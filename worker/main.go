package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

type OutboxEvent struct {
	ID      string         `json:"outbox_event_id"`
	Type    string         `json:"event_type"`
	Payload map[string]any `json:"payload"`
}

type NotificationMessage struct {
	NotificationID int64  `json:"notification_id"`
	To             string `json:"to"`
	Type           string `json:"type"`
	ApplicationID  string `json:"application_id"`
	Message        string `json:"message"`
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func connectDB(ctx context.Context) (*pgxpool.Pool, error) {
	var lastErr error
	for attempt := 1; attempt <= 60; attempt++ {
		db, err := pgxpool.New(ctx, os.Getenv("WORKER_DATABASE_URL"))
		if err == nil {
			err = db.Ping(ctx)
			if err == nil {
				return db, nil
			}
			db.Close()
		}
		lastErr = err
		log.Printf("worker database connection attempt %d/60 failed", attempt)
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("database unavailable: %w", lastErr)
}

func connectRabbit() (*amqp.Connection, error) {
	var lastErr error
	for attempt := 1; attempt <= 60; attempt++ {
		conn, err := amqp.Dial(os.Getenv("AMQP_URL"))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		log.Printf("worker RabbitMQ connection attempt %d/60 failed", attempt)
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("RabbitMQ unavailable: %w", lastErr)
}

func main() {
	ctx := context.Background()
	db, err := connectDB(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	conn, err := connectRabbit()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		log.Fatal(err)
	}
	defer ch.Close()
	if _, err = ch.QueueDeclare("document.notifications", true, false, false, false, nil); err != nil {
		log.Fatal(err)
	}
	if err = ch.Confirm(false); err != nil {
		log.Fatalf("enable publisher confirmations: %v", err)
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	if err = ch.Qos(1, 0, false); err != nil {
		log.Fatal(err)
	}
	deliveries, err := ch.Consume("document.notifications", "documents-worker", false, false, false, false, nil)
	if err != nil {
		log.Fatal(err)
	}

	// The consumer acknowledges only after SMTP delivery and a durable status update.
	// Delivery is at-least-once: the notification ID is stable across broker retries.
	go consume(ctx, db, deliveries)
	log.Printf("worker ready; queue=document.notifications; smtp=%s", env("MAIL_SMTP_ADDR", "mailpit:1025"))

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := publishPending(ctx, db, ch, confirms); err != nil {
			log.Printf("outbox pass failed: %v", err)
		}
	}
}

func publishPending(ctx context.Context, db *pgxpool.Pool, ch *amqp.Channel, confirms <-chan amqp.Confirmation) error {
	rows, err := db.Query(ctx, `SELECT outbox_event_id::text,event_type,payload FROM outbox_events WHERE published_at IS NULL ORDER BY created_at LIMIT 25`)
	if err != nil {
		return fmt.Errorf("read outbox: %w", err)
	}
	events := make([]OutboxEvent, 0)
	for rows.Next() {
		var event OutboxEvent
		var raw []byte
		if err := rows.Scan(&event.ID, &event.Type, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("scan outbox: %w", err)
		}
		if err := json.Unmarshal(raw, &event.Payload); err != nil {
			log.Printf("invalid outbox JSON event_id=%s; leaving visible for operator: %v", event.ID, err)
			continue
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, event := range events {
		if event.Type != "document_ready" && event.Type != "status_changed" {
			_, err := db.Exec(ctx, `UPDATE outbox_events SET published_at=now(),attempts=attempts+1,last_error='unsupported event type' WHERE outbox_event_id=$1::uuid`, event.ID)
			if err != nil {
				log.Printf("mark unsupported outbox event failed id=%s: %v", event.ID, err)
			}
			continue
		}
		applicationID, _ := event.Payload["application_id"].(string)
		ownerID, _ := event.Payload["owner_user_id"].(string)
		if applicationID == "" {
			markOutboxError(ctx, db, event.ID, "missing application_id")
			continue
		}
		var email string
		if ownerID != "" {
			err = db.QueryRow(ctx, `SELECT email FROM users WHERE user_id=$1::uuid`, ownerID).Scan(&email)
		} else {
			err = db.QueryRow(ctx, `SELECT u.email FROM applications a JOIN users u ON u.user_id=a.owner_user_id WHERE a.application_id=$1::uuid`, applicationID).Scan(&email)
		}
		if err != nil {
			markOutboxError(ctx, db, event.ID, "recipient lookup failed")
			log.Printf("recipient lookup event_id=%s failed: %v", event.ID, err)
			continue
		}

		notificationType := "status_changed"
		if event.Type == "document_ready" {
			notificationType = "document_ready"
		}
		var notificationID int64
		// source_outbox_event_id is unique, so retrying a publisher pass reuses the
		// same notification row and message ID rather than inserting a duplicate.
		err = db.QueryRow(ctx, `INSERT INTO notifications(application_id,recipient_user_id,recipient_address,notification_type,status,source_outbox_event_id)
			SELECT $1::uuid,u.user_id,$2,$3,'pending',$4::uuid FROM users u WHERE u.email=$2
			ON CONFLICT(source_outbox_event_id) DO UPDATE SET source_outbox_event_id=EXCLUDED.source_outbox_event_id
			RETURNING notification_id`, applicationID, email, notificationType, event.ID).Scan(&notificationID)
		if err != nil {
			markOutboxError(ctx, db, event.ID, "notification create failed")
			log.Printf("notification create event_id=%s failed: %v", event.ID, err)
			continue
		}
		message := NotificationMessage{NotificationID: notificationID, To: email, Type: notificationType, ApplicationID: applicationID, Message: "В системе доступно обновление по заявке."}
		body, err := json.Marshal(message)
		if err != nil {
			markOutboxError(ctx, db, event.ID, "message encoding failed")
			continue
		}
		sequence := ch.GetNextPublishSeqNo()
		err = ch.PublishWithContext(ctx, "", "document.notifications", false, false, amqp.Publishing{
			ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: event.ID, Type: event.Type, Body: body,
			Timestamp: time.Now().UTC(),
		})
		if err == nil {
			deadline := time.NewTimer(5 * time.Second)
		confirmLoop:
			for {
				select {
				case confirmation, ok := <-confirms:
					if !ok {
						err = errors.New("RabbitMQ confirmation channel closed")
						break confirmLoop
					}
					if confirmation.DeliveryTag < sequence {
						continue
					} // stale confirm from a timed-out publish
					if !confirmation.Ack {
						err = errors.New("RabbitMQ negatively acknowledged published event")
					}
					break confirmLoop
				case <-deadline.C:
					err = errors.New("timed out waiting for RabbitMQ publisher confirmation")
					break confirmLoop
				case <-ctx.Done():
					err = ctx.Err()
					break confirmLoop
				}
			}
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
		}
		if err != nil {
			markOutboxError(ctx, db, event.ID, "broker publish/confirm failed")
			log.Printf("publish event_id=%s failed: %v", event.ID, err)
			continue
		}
		_, err = db.Exec(ctx, `UPDATE outbox_events SET published_at=now(),attempts=attempts+1,last_error=NULL WHERE outbox_event_id=$1::uuid AND published_at IS NULL`, event.ID)
		if err != nil {
			log.Printf("mark published event_id=%s failed (message may be redelivered): %v", event.ID, err)
		}
	}
	return nil
}

func markOutboxError(ctx context.Context, db *pgxpool.Pool, eventID, message string) {
	if _, err := db.Exec(ctx, `UPDATE outbox_events SET attempts=attempts+1,last_error=$2 WHERE outbox_event_id=$1::uuid`, eventID, message); err != nil {
		log.Printf("record outbox failure event_id=%s failed: %v", eventID, err)
	}
}

func consume(ctx context.Context, db *pgxpool.Pool, deliveries <-chan amqp.Delivery) {
	for delivery := range deliveries {
		var message NotificationMessage
		if err := json.Unmarshal(delivery.Body, &message); err != nil || message.NotificationID <= 0 {
			log.Printf("reject malformed notification message_id=%s", delivery.MessageId)
			_ = delivery.Reject(false)
			continue
		}
		var status string
		err := db.QueryRow(ctx, `SELECT status FROM notifications WHERE notification_id=$1`, message.NotificationID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf("notification row missing id=%d; rejecting without requeue", message.NotificationID)
			_ = delivery.Reject(false)
			continue
		}
		if err != nil {
			log.Printf("read notification status id=%d failed: %v", message.NotificationID, err)
			_ = delivery.Nack(false, true)
			continue
		}
		if status == "sent" {
			_ = delivery.Ack(false)
			continue
		} // broker redelivery after a prior successful update
		if err := sendMail(message); err != nil {
			log.Printf("SMTP notification id=%d failed: %v", message.NotificationID, err)
			_, _ = db.Exec(ctx, `UPDATE notifications SET status='failed',last_error='local SMTP delivery failed' WHERE notification_id=$1 AND status<>'sent'`, message.NotificationID)
			// Basic retry backoff avoids a tight requeue loop while Mailpit/SMTP is unavailable.
			time.Sleep(2 * time.Second)
			_ = delivery.Nack(false, true)
			continue
		}
		if _, err := db.Exec(ctx, `UPDATE notifications SET status='sent',sent_at=now(),last_error=NULL WHERE notification_id=$1 AND status<>'sent'`, message.NotificationID); err != nil {
			log.Printf("mark notification sent id=%d failed: %v", message.NotificationID, err)
			_ = delivery.Nack(false, true)
			continue
		}
		log.Printf("local SMTP notification delivered notification_id=%d type=%s application_id=%s", message.NotificationID, message.Type, message.ApplicationID)
		_ = delivery.Ack(false)
	}
}

func sendMail(message NotificationMessage) error {
	parsed, err := mail.ParseAddress(message.To)
	if err != nil {
		return fmt.Errorf("invalid recipient address")
	}
	from := env("MAIL_FROM", "documents-system@localhost")
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("invalid configured sender")
	}
	subject := "Обновление заявки в системе документов"
	if message.Type == "document_ready" {
		subject = "Документ готов к получению"
	}
	// Message fields are not copied into header lines; CR/LF in DB values cannot
	// create additional SMTP headers.
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\nНомер заявки: %s\r\n", from, parsed.Address, subject, strings.TrimSpace(message.Message), message.ApplicationID)
	if err := smtp.SendMail(env("MAIL_SMTP_ADDR", "mailpit:1025"), nil, from, []string{parsed.Address}, []byte(body)); err != nil {
		return err
	}
	return nil
}
