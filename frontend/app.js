let currentStack = null;
let logWs = null;
let shellWs = null;
let term = null;
let statusInterval = null;
let currentLogsContainer = null;
let currentShellContainer = null;
let containers = [];

async function loadStacks() {
    const res = await fetch('/api/stacks');
    const stacks = await res.json();
    const list = document.getElementById('stack-list');
    
    list.innerHTML = '';
    
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
    currentLogsContainer = null;
    currentShellContainer = null;
    containers = [];
    
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
    if (!input || !outputBlock) return;

    let content = input.value;

    // Sync scrolling
    outputBlock.parentElement.scrollTop = input.scrollTop;
    outputBlock.parentElement.scrollLeft = input.scrollLeft;

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

async function loadContainers() {
    if (!currentStack) return;
    try {
        const res = await fetch(`/api/stack/containers?name=${currentStack}`);
        containers = await res.json();
        
        // Update logs dropdown
        const logsSelect = document.getElementById('logs-container-select');
        logsSelect.innerHTML = '';
        containers.forEach((c, idx) => {
            const option = document.createElement('option');
            option.value = c.id;
            option.textContent = c.name;
            if (idx === 0 && !currentLogsContainer) {
                option.selected = true;
                currentLogsContainer = c.id;
            } else if (c.id === currentLogsContainer) {
                option.selected = true;
            }
            logsSelect.appendChild(option);
        });
        
        // Update shell dropdown
        const shellSelect = document.getElementById('shell-container-select');
        shellSelect.innerHTML = '';
        containers.forEach((c, idx) => {
            const option = document.createElement('option');
            option.value = c.id;
            option.textContent = c.name;
            if (idx === 0 && !currentShellContainer) {
                option.selected = true;
                currentShellContainer = c.id;
            } else if (c.id === currentShellContainer) {
                option.selected = true;
            }
            shellSelect.appendChild(option);
        });
    } catch (error) {
        console.error('Failed to load containers:', error);
    }
}

function switchTab(tab) {
    ['stack', 'logs', 'shell'].forEach(t => {
        document.getElementById(`pane-${t}`).classList.add('hidden');
        document.getElementById(`tab-${t}`).className = "px-6 py-1.5 rounded-pill text-xs font-bold transition-all text-ctp-subtext0 hover:bg-ctp-surface1";
    });
    
    document.getElementById(`pane-${tab}`).classList.remove('hidden');
    document.getElementById(`tab-${tab}`).className = "px-6 py-1.5 rounded-pill text-xs font-bold transition-all tab-active";

    stopStreams();
    if (tab === 'logs') {
        loadContainers().then(() => startLogs());
    }
    if (tab === 'shell') {
        loadContainers().then(() => startShell());
    }
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
    if (!currentStack) return;
    
    const btn = document.getElementById('save-btn');
    const originalContent = btn.innerHTML;
    const originalClass = btn.className;
    
    // Show loading state
    btn.innerHTML = '<i class="fas fa-circle-notch fa-spin text-[10px]"></i> SAVING';
    btn.className = "bg-ctp-blue/10 text-ctp-blue px-6 py-2 rounded-pill text-xs font-bold transition-all flex items-center gap-2";
    
    try {
        const yaml = document.getElementById('yaml-input').value;
        const env = document.getElementById('env-input').value;
        const res = await fetch('/api/stack/save', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: currentStack, yaml, env })
        });
        
        if (!res.ok) {
            throw new Error(`Save failed: ${res.status} ${res.statusText}`);
        }
        
        // Show success state
        btn.innerHTML = '<i class="fas fa-check text-[10px]"></i> DONE';
        btn.className = "bg-ctp-green/10 text-ctp-green px-6 py-2 rounded-pill text-xs font-bold transition-all flex items-center gap-2";
        
        // Revert to original after 2 seconds
        setTimeout(() => {
            btn.innerHTML = originalContent;
            btn.className = originalClass;
        }, 2000);
    } catch (error) {
        // Show error alert
        alert(`Failed to save stack: ${error.message}`);
        
        // Revert to original state
        btn.innerHTML = originalContent;
        btn.className = originalClass;
    }
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
    const logsContent = document.getElementById('logs-content');
    const select = document.getElementById('logs-container-select');
    const containerID = select.value || (containers.length > 0 ? containers[0].id : null);
    
    if (!containerID) {
        logsContent.innerHTML = '<div class="opacity-50 italic">No containers available</div>';
        return;
    }
    
    currentLogsContainer = containerID;
    logsContent.innerHTML = '<div class="opacity-50 italic">... attaching to logs</div>';
    
    // Initialize ANSI to HTML converter
    const ansiUp = new AnsiUp();
    
    if (logWs) logWs.close();
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    logWs = new WebSocket(`${wsProtocol}//${window.location.host}/ws/logs?name=${currentStack}&container=${containerID}`);
    logWs.onmessage = (e) => {
        const div = document.createElement('div');
        div.className = "mb-1";
        div.innerHTML = ansiUp.ansi_to_html(e.data);
        logsContent.appendChild(div);
        logsContent.scrollTop = logsContent.scrollHeight;
    };
    logWs.onerror = () => {
        logsContent.innerHTML = '<div class="opacity-50 italic">Error connecting to logs</div>';
    };
}

function startShell() {
    const select = document.getElementById('shell-container-select');
    const containerID = select.value || (containers.length > 0 ? containers[0].id : null);
    
    if (!containerID) {
        const terminalDiv = document.getElementById('terminal');
        terminalDiv.innerHTML = '<div class="opacity-50 italic">No containers available</div>';
        return;
    }
    
    currentShellContainer = containerID;
    
    if (term) term.dispose();
    term = new Terminal({
        theme: { background: '#11111b', foreground: '#cdd6f4', cursor: '#cba6f7' },
        fontFamily: 'JetBrains Mono',
        fontSize: 13,
        rows: 24,
        cols: 80
    });
    term.open(document.getElementById('terminal'));
    
    if (shellWs) shellWs.close();
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    shellWs = new WebSocket(`${wsProtocol}//${window.location.host}/ws/shell?name=${currentStack}&container=${containerID}`);
    shellWs.onmessage = (e) => term.write(e.data);
    shellWs.onerror = () => {
        term.write('\r\nError connecting to shell\r\n');
    };
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
    
    // Setup container dropdown change handlers
    document.getElementById('logs-container-select').addEventListener('change', () => {
        stopStreams();
        startLogs();
    });
    
    document.getElementById('shell-container-select').addEventListener('change', () => {
        stopStreams();
        startShell();
    });
};
