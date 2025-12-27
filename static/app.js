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
        btn.className = `w-full flex items-center px-4 py-3 rounded-pill transition-all ${activeClass}`;
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
    
    // Setup scroll sync after loading content
    setupEditorScrollSync();
}

function updateHighlight(id) {
    const input = document.getElementById(`${id}-input`);
    const outputBlock = document.getElementById(`${id}-output-block`);
    const outputPre = document.getElementById(`${id}-output-pre`);
    if (!input || !outputBlock || !outputPre) return;

    let content = input.value;

    // Sync scrolling
    outputPre.scrollTop = input.scrollTop;
    outputPre.scrollLeft = input.scrollLeft;

    const lang = id === 'yaml' ? 'yaml' : 'ini';
    const highlighted = hljs.highlight(content, { language: lang }).value;
    outputBlock.innerHTML = highlighted + (content.endsWith('\n') ? '\n' : '');
}

function setupEditorScrollSync() {
    ['yaml', 'env'].forEach(id => {
        const input = document.getElementById(`${id}-input`);
        const outputPre = document.getElementById(`${id}-output-pre`);
        if (input && outputPre) {
            input.addEventListener('scroll', () => {
                outputPre.scrollTop = input.scrollTop;
                outputPre.scrollLeft = input.scrollLeft;
            });
        }
    });
}

function switchTab(tab) {
    ['stack', 'logs', 'shell'].forEach(t => {
        document.getElementById(`pane-${t}`).classList.add('hidden');
        document.getElementById(`tab-${t}`).className = "px-6 py-1.5 rounded-pill text-xs font-bold transition-all text-ctp-subtext0 hover:bg-ctp-surface1";
    });
    
    document.getElementById(`pane-${tab}`).classList.remove('hidden');
    document.getElementById(`tab-${tab}`).className = "px-6 py-1.5 rounded-pill text-xs font-bold transition-all tab-active";

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
        dot.className = "w-2 h-2 bg-ctp-green rounded-full";
        text.className = "text-[11px] text-ctp-green font-bold uppercase tracking-wider";
        text.innerText = "Operational";
        btn.className = "bg-ctp-red/10 text-ctp-red hover:bg-ctp-red hover:text-ctp-base px-6 py-2 rounded-pill text-xs font-bold transition-all flex items-center gap-2";
        btn.innerHTML = '<i class="fas fa-stop text-[10px]"></i> STOP';
    } else {
        dot.className = "w-2 h-2 bg-ctp-surface2 rounded-full";
        text.className = "text-[11px] text-ctp-subtext0 font-bold uppercase tracking-wider";
        text.innerText = "Stopped";
        btn.className = "bg-ctp-green/10 text-ctp-green hover:bg-ctp-green hover:text-ctp-base px-6 py-2 rounded-pill text-xs font-bold transition-all flex items-center gap-2";
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
    performAction(status.toLowerCase() === 'operational' ? 'stop' : 'start');
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
    const modal = document.getElementById('new-stack-modal');
    const input = document.getElementById('new-stack-name');
    modal.classList.remove('hidden');
    input.value = '';
    input.focus();
    
    // Allow Enter key to submit
    input.onkeypress = (e) => {
        if (e.key === 'Enter') {
            confirmNewStack();
        }
    };
    
    // Allow Escape key to cancel
    const escHandler = (e) => {
        if (e.key === 'Escape') {
            cancelNewStack();
            document.removeEventListener('keydown', escHandler);
        }
    };
    document.addEventListener('keydown', escHandler);
}

function cancelNewStack() {
    const modal = document.getElementById('new-stack-modal');
    modal.classList.add('hidden');
}

async function confirmNewStack() {
    const input = document.getElementById('new-stack-name');
    const name = input.value.trim();
    
    if (!name) {
        input.classList.add('border-ctp-red');
        input.placeholder = 'Stack name is required!';
        setTimeout(() => {
            input.classList.remove('border-ctp-red');
            input.placeholder = 'my-awesome-stack';
        }, 2000);
        return;
    }
    
    // Validate stack name (alphanumeric, dashes, underscores)
    if (!/^[a-z0-9-_]+$/i.test(name)) {
        input.classList.add('border-ctp-red');
        const oldPlaceholder = input.placeholder;
        input.placeholder = 'Use only letters, numbers, dashes, and underscores';
        setTimeout(() => {
            input.classList.remove('border-ctp-red');
            input.placeholder = oldPlaceholder;
        }, 2000);
        return;
    }
    
    cancelNewStack();
    
    // Create the stack with default template
    currentStack = name;
    document.getElementById('yaml-input').value = `version: '3.9'

services:
  app:
    image: nginx:latest
    container_name: ${name}_app
    restart: unless-stopped
    ports:
      - "80:80"
    environment:
      - PUID=\${PUID}
      - PGID=\${PGID}
    labels:
      - "bunshin.managed=true"`;
    
    document.getElementById('env-input').value = `# Environment Variables
PUID=1000
PGID=1000`;
    
    await saveStack();
    await loadStacks();
    selectStack(name);
}

window.onload = () => {
    loadStacks();
    setupEditorScrollSync();
};
