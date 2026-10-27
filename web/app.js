  'use strict';
  let token = '';
  async function api(path, method = 'GET', body) {
    const options = {method, headers: {'Accept':'application/json'}};
    if (token) options.headers.Authorization = 'Bearer ' + token;
    if (body !== undefined) { options.headers['Content-Type'] = 'application/json'; options.body = JSON.stringify(body); }
    try {
      const response = await fetch('/api/v1' + path, options);
      const raw = await response.text();
      let data; try { data = raw ? JSON.parse(raw) : null; } catch { data = raw; }
      document.querySelector('#out').textContent = JSON.stringify({http_status:response.status, request_id:response.headers.get('X-Request-ID'), response:data}, null, 2);
      return {status:response.status, data};
    } catch (error) {
      document.querySelector('#out').textContent = 'Ошибка соединения: ' + String(error);
      return {status:0, data:null};
    }
  }
  async function login() {
    const result = await api('/auth/login','POST',{university_id:document.querySelector('#uid').value,password:document.querySelector('#pwd').value});
    if (result.data && result.data.access_token) {
      token = result.data.access_token;
      document.querySelector('#who').textContent = result.data.user.full_name + ' (' + result.data.user.role + ')';
    }
  }
  async function logout() { if (!token) { document.querySelector('#who').textContent = 'Нет активной сессии'; return; } await api('/auth/logout','POST',{}); token=''; document.querySelector('#who').textContent='Нет активной сессии в этом окне.'; }
  async function createApp() {
    const r = await api('/applications','POST',{document_type_code:document.querySelector('#type').value,comment:document.querySelector('#comment').value});
    if (r.data && r.data.application_id) document.querySelector('#appId').value = r.data.application_id;
  }
  function listApps() {
    const params = new URLSearchParams({page:document.querySelector('#page').value || '1',page_size:document.querySelector('#pageSize').value || '20'});
    for (const [id,key] of [['filterStatus','status'],['filterType','document_type_code'],['createdFrom','created_from'],['createdTo','created_to']]) { const value=document.querySelector('#'+id).value.trim(); if(value) params.set(key,value); }
    api('/applications?'+params.toString());
  }
  function getApp() { api('/applications/'+encodeURIComponent(document.querySelector('#appId').value.trim())); }
  function changeStatus() { api('/applications/'+encodeURIComponent(document.querySelector('#appId').value.trim())+'/status','PATCH',{status:document.querySelector('#newStatus').value,reason:document.querySelector('#reason').value}); }
  async function issue() {
    const r=await api('/applications/'+encodeURIComponent(document.querySelector('#appId').value.trim())+'/issue','POST',{document_title:document.querySelector('#documentTitle').value});
    if(r.data && r.data.document_id) document.querySelector('#docId').value=r.data.document_id;
  }
  function getDocument() { api('/documents/'+encodeURIComponent(document.querySelector('#docId').value.trim())); }
  function audit() { api('/audit/events'); }
  function recoveryRequest() { api('/auth/recovery','POST',{university_id:document.querySelector('#recoveryUid').value.trim()}); }
  function recoveryConfirm() { api('/auth/recovery/confirm','POST',{code:document.querySelector('#recoveryCode').value.trim(),new_password:document.querySelector('#newPassword').value}); }

// Event handlers are external so the browser can enforce a strict CSP.
document.addEventListener('click', (event) => {
  const button = event.target.closest('[data-action]');
  if (!button) return;
  const actions = {login, logout, recoveryRequest, recoveryConfirm, createApp, listApps, getApp, changeStatus, issue, getDocument, audit};
  const action = actions[button.dataset.action];
  if (typeof action === 'function') action();
});
