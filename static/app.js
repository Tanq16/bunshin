let currentStack = null;
let logWs = null;
let shellWs = null;
let term = null;
let statusInterval = null;

async function loadStacks() {
    const res = await fetch('/api/stacks');
    const stacks = await res.json();
    const list = document.getElementById('stack-list');
    
    list.innerHTML = '<div class="text-[10px] uppercase tracking-widest text-ctp-overlay0 font-bold px-4 mb-2">My Stacks</div>';
    
    stacks.forEach(s => {
        const btn = document.createElement('button');
        btn.onclick = () => selectStack(s);
        const activeClass = currentStack === s ? 'bg-ctp-surface0 text-ctp-text' : 'hover:bg-ctp-surface0/50 text-ctp-subtext0';
        btn.className = `w-full flex items-center px-4 py-3 pill transition-all ${activeClass}`;
        btn.innerHTML = `
            <i class="fas fa-server mr-3 ${currentStack === s ? 'text-ctp-green' : 'text-ctp-blue'} text-sm"></i>
            <span class="text-sm font-medium">${s}</span>
        `;
        list.appendChild(btn);
    });
}

async function selectStack(name) {
    currentStack = name;
    document.getElementById('current-stack-title').innerText = name;
    
    document.getElementById('header-info').classList.remove('invisible');
    document.getElementById('tabs-container').classList.remove('invisible');
    document.getElementById('actions-container').classList.remove('invisible');
    
    const res = await fetch(`/api/stack/get?name=${name}`);
    const data = await res.json();
    
    document.getElementById('yaml-input').value = data.yaml || '';
    document.getElementById('env-input').value = data.env || '';
    
    loadStacks();
    updateHighlight('yaml');
    updateHighlight('env');
    switchTab('stack');
    updateStatus();
    
    if (statusInterval) clearInterval(statusInterval);
    statusInterval = setInterval(updateStatus, 5000);
}

function updateHighlight(id) {
    const input = document.getElementById(`${id}-input`);
    const output = document.getElementById(`${id}-output-block`);
    const lang = id === 'yaml' ? 'yaml' : 'ini';
    output.innerHTML = hljs.highlight(input.value, { language: lang }).value;
}

function switchTab(tab) {
    ['stack', 'logs', 'shell'].forEach(t => {
        document.getElementById(`pane-${t}`).classList.add('hidden');
        document.getElementById(`tab-${t}`).className = "px-6 py-1.5 pill text-xs font-bold transition-all text-ctp-subtext0 hover:bg-ctp-surface1";
    });
    
    document.getElementById(`pane-${tab}`).classList.remove('hidden');
    document.getElementById(`tab-${tab}`).className = "px-6 py-1.5 pill text-xs font-bold transition-all tab-active";

    stopStreams();
    if (tab === 'logs') startLogs();
    if (tab === 'shell') startShell();
}

async function updateStatus() {
    if (!currentStack) return;
    const res = await fetch(`/api/stack/status?name=${currentStack}`);
    const status = await res.text();
    
    const dot = document.getElementById('status-dot');
    const text = document.getElementById('status-text');
    const btn = document.getElementById('toggle-btn');

    if (status === 'Operational') {
        dot.className = "w-2 h-2 rounded-full bg-ctp-green";
        text.className = "text-[11px] text-ctp-green font-bold uppercase tracking-wider";
        text.innerText = "Operational";
        btn.className = "bg-ctp-red/10 text-ctp-red hover:bg-ctp-red hover:text-ctp-base px-6 py-2 pill text-xs font-bold transition-all flex items-center gap-2";
        btn.innerHTML = '<i class="fas fa-stop text-[10px]"></i> STOP';
    } else {
        dot.className = "w-2 h-2 rounded-full bg-ctp-surface2";
        text.className = "text-[11px] text-ctp-subtext0 font-bold uppercase tracking-wider";
        text.innerText = "Stopped";
        btn.className = "bg-ctp-green/10 text-ctp-green hover:bg-ctp-green hover:text-ctp-base px-6 py-2 pill text-xs font-bold transition-all flex items-center gap-2";
        btn.innerHTML = '<i class="fas fa-play text-[10px]"></i> START';
    }
}

async function saveStack() {
    const yaml = document.getElementById('yaml-input').value;
    const env = document.getElementById('env-input').value;
    await fetch('/api/stack/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: currentStack, yaml, env })
    });
}

async function performAction(action) {
    if (!currentStack) return;
    const btn = document.getElementById('toggle-btn');
    const originalContent = btn.innerHTML;
    btn.innerHTML = '<i class="fas fa-circle-notch fa-spin text-[10px]"></i> WAIT';
    
    await fetch(`/api/stack/action?name=${currentStack}&action=${action}`, { method: 'POST' });
    updateStatus();
}

function toggleStack() {
    const status = document.getElementById('status-text').innerText;
    performAction(status === 'OPERATIONAL' ? 'stop' : 'start');
}

function startLogs() {
    const pane = document.getElementById('pane-logs');
    pane.innerHTML = '<div class="opacity-50 italic">... attaching to logs</div>';
    
    logWs = new WebSocket(`ws://${window.location.host}/ws/logs?name=${currentStack}`);
    logWs.onmessage = (e) => {
        const div = document.createElement('div');
        div.className = "mb-1";
        div.innerText = e.data;
        pane.appendChild(div);
        pane.scrollTop = pane.scrollHeight;
    };
}

function startShell() {
    if (term) term.dispose();
    term = new Terminal({
        theme: { background: '#11111b', foreground: '#cdd6f4', cursor: '#cba6f7' },
        fontFamily: 'JetBrains Mono',
        fontSize: 13,
        rows: 24,
        cols: 80
    });
    term.open(document.getElementById('terminal'));
    
    shellWs = new WebSocket(`ws://${window.location.host}/ws/shell?name=${currentStack}`);
    shellWs.onmessage = (e) => term.write(e.data);
    term.onData(data => { if (shellWs.readyState === 1) shellWs.send(data); });
}

function stopStreams() {
    if (logWs) logWs.close();
    if (shellWs) shellWs.close();
}

function createNewStack() {
    const name = prompt("Enter stack name:");
    if (name) {
        currentStack = name;
        document.getElementById('yaml-input').value = "services:\n  app:\n    image: nginx";
        document.getElementById('env-input').value = "";
        saveStack().then(loadStacks);
        selectStack(name);
    }
}

window.onload = loadStacks;
