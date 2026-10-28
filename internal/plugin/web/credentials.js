async function sha256Hex(value) {
  const bytes = new TextEncoder().encode(value);
  if (window.crypto?.subtle) {
    const digest = await crypto.subtle.digest("SHA-256", bytes);
    return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
  }
  // Public HTTP origins lack SubtleCrypto; identity hashes must still match.
  const constants = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be,
    0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa,
    0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85,
    0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
    0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f,
    0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ];
  const hash = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
  const padded = new Uint8Array(Math.ceil((bytes.length + 9) / 64) * 64);
  padded.set(bytes);
  padded[bytes.length] = 0x80;
  const data = new DataView(padded.buffer);
  const bitLength = bytes.length * 8;
  data.setUint32(padded.length - 8, Math.floor(bitLength / 0x100000000));
  data.setUint32(padded.length - 4, bitLength >>> 0);
  const words = new Uint32Array(64);
  const rotate = (word, bits) => (word >>> bits) | (word << (32 - bits));
  for (let offset = 0; offset < padded.length; offset += 64) {
    for (let i = 0; i < 16; i++) words[i] = data.getUint32(offset + i * 4);
    for (let i = 16; i < 64; i++) {
      const x = words[i - 15], y = words[i - 2];
      const s0 = rotate(x, 7) ^ rotate(x, 18) ^ (x >>> 3);
      const s1 = rotate(y, 17) ^ rotate(y, 19) ^ (y >>> 10);
      words[i] = words[i - 16] + s0 + words[i - 7] + s1;
    }
    let [a, b, c, d, e, f, g, h] = hash;
    for (let i = 0; i < 64; i++) {
      const sum1 = rotate(e, 6) ^ rotate(e, 11) ^ rotate(e, 25);
      const choice = (e & f) ^ (~e & g);
      const t1 = (h + sum1 + choice + constants[i] + words[i]) >>> 0;
      const sum0 = rotate(a, 2) ^ rotate(a, 13) ^ rotate(a, 22);
      const majority = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (sum0 + majority) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    const block = [a, b, c, d, e, f, g, h];
    for (let i = 0; i < 8; i++) hash[i] += block[i];
  }
  return Array.from(hash, (word) => word.toString(16).padStart(8, "0")).join("");
}

function credentialName(item) { return window.billingI18n.serverMessage(item.display_name_message, item.display_name); }

function credentialLabel(item) { return m("ui.join", { v0: item.provider_name, v1: DISPLAY_SEPARATOR, v2: credentialName(item) }); }

function credentialSourceLabel(source) { return source === "auth-files" ? m("ui.auth_files") : m("ui.ai_providers"); }

function credentialProviderLabel(item) {
  return m("ui.join", { v0: credentialSourceLabel(item.source), v1: DISPLAY_SEPARATOR, v2: item.provider });
}

function credentialIcon(item) { return item.source === "auth-files" ? "file-key" : "key"; }

function credentialSourceRank(item) { return item.source === "auth-files" ? 0 : 1; }

function compareCredentialProviders(a, b) {
  return credentialSourceRank(a) - credentialSourceRank(b) || a.provider.localeCompare(b.provider);
}

function compareCredentials(a, b) {
  return (
    credentialSourceRank(a) - credentialSourceRank(b) || String(credentialLabel(a)).localeCompare(String(credentialLabel(b)), locale())
  );
}

function sameCredentialProvider(a, b) { return a.source === b.source && a.provider === b.provider; }

function clearCredentialScope(account) {
  const role = account ? "account" : "admin";
  const key = authQuotaStorageKey(role);
  if (key) { try { sessionStorage.removeItem(key); } catch (_) {} }
  credentialScope[role] = "";
  resetAuthQuotaMemory(account);
}

async function activateCredentialScope(account, credential) {
  const generation = sessionGeneration;
  const role = account ? "account" : "admin";
  resetAuthQuotaMemory(account);
  if (!account) resetAIBalanceBatch();
  credentialScope[role] = "";
  try {
    const scope = (await sha256Hex(role + "|" + credential)).slice(0, 32);
    if (generation !== sessionGeneration) throw new StaleRequestError();
    credentialScope[role] = scope;
  } catch (_) {
    if (generation !== sessionGeneration) throw new StaleRequestError();
    return;
  }
  restoreAuthQuotaCache(account);
}

const PROVIDER_LABELS = {
  claude: "Claude",
  antigravity: "Antigravity",
  codex: "Codex",
  gemini: "Gemini",
  xai: "xAI",
  kimi: "Kimi",
  vertex: "Vertex",
  meta: "Meta",
  openai: "OpenAI"
};

function credentialRoutingTag(credential) {
  const priority = credential.priority ?? "—";
  const weight = credential.weight ?? "—";
  return metricTag([["P", priority], ["W", weight]], m("ui.credential_routing", { priority, weight }));
}

const PROVIDER_ICONS = {
  claude: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M4.709 15.955l4.72-2.647.08-.23-.08-.128H9.2l-.79-.048-2.698-.073-2.339-.097-2.266-.122-.571-.121L0 11.784l.055-.352.48-.321.686.06 1.52.103 2.278.158 1.652.097 2.449.255h.389l.055-.157-.134-.098-.103-.097-2.358-1.596-2.552-1.688-1.336-.972-.724-.491-.364-.462-.158-1.008.656-.722.881.06.225.061.893.686 1.908 1.476 2.491 1.833.365.304.145-.103.019-.073-.164-.274-1.355-2.446-1.446-2.49-.644-1.032-.17-.619a2.97 2.97 0 01-.104-.729L6.283.134 6.696 0l.996.134.42.364.62 1.414 1.002 2.229 1.555 3.03.456.898.243.832.091.255h.158V9.01l.128-1.706.237-2.095.23-2.695.08-.76.376-.91.747-.492.584.28.48.685-.067.444-.286 1.851-.559 2.903-.364 1.942h.212l.243-.242.985-1.306 1.652-2.064.73-.82.85-.904.547-.431h1.033l.76 1.129-.34 1.166-1.064 1.347-.881 1.142-1.264 1.7-.79 1.36.073.11.188-.02 2.856-.606 1.543-.28 1.841-.315.833.388.091.395-.328.807-1.969.486-2.309.462-3.439.813-.042.03.049.061 1.549.146.662.036h1.622l3.02.225.79.522.474.638-.079.485-1.215.62-1.64-.389-3.829-.91-1.312-.329h-.182v.11l1.093 1.068 2.006 1.81 2.509 2.33.127.578-.322.455-.34-.049-2.205-1.657-.851-.747-1.926-1.62h-.128v.17l.444.649 2.345 3.521.122 1.08-.17.353-.608.213-.668-.122-1.374-1.925-1.415-2.167-1.143-1.943-.14.08-.674 7.254-.316.37-.729.28-.607-.461-.322-.747.322-1.476.389-1.924.315-1.53.286-1.9.17-.632-.012-.042-.14.018-1.434 1.967-2.18 2.945-1.726 1.845-.414.164-.717-.37.067-.662.401-.589 2.388-3.036 1.44-1.882.93-1.086-.006-.158h-.055L4.132 18.56l-1.13.146-.487-.456.061-.746.231-.243 1.908-1.312-.006.006z"/></svg>`,
  antigravity: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 59" aria-hidden="true"><path fill="#3789F9" transform="translate(28,0)" d="M0 0h8l6 4 5 10 8 26 5 10 4 4-1 5h-5l-8-7-11-17-5-2-7 1-5 5-8 13-8 7h-6l1-6 5-6 5-13 7-22 5-9z"/><path fill="#6D80D8" transform="translate(28,0)" d="M0 0h8l6 4 5 10 6 21-4-1-5-5-5-3-4-6v-2l-5-2-5-1-5 3-4 1 3-10 5-7z"/><path fill="#D78240" transform="translate(28,0)" d="M0 0h8l6 4 5 10 1 5-7-4-3-3-7-2-4-2-6-1 3-5z"/><path fill="#3294CC" transform="translate(25,14)" d="M0 0l5 1 5 3 2 5-11-1-6 5-5 8-3 5h-3l7-21 5-3z"/><path fill="#E45C49" transform="translate(36,1)" d="M0 0l5 2 5 8 2 8-7-4-4-4-1-6-3-1 3-1z"/><path fill="#90AE64" transform="translate(21,7)" d="M0 0l9 1 3 2v2l-5 1-3 2-5 3-4 1 3-10z"/><path fill="#53A89A" transform="translate(25,14)" d="M0 0l5 1v3l-7 3-5 4-4-1 2-5 5-3z"/><path fill="#B5677D" transform="translate(33,11)" d="M0 0h5l11 9 1 4-5-1-4-3V7L4 5 0 2z"/><path fill="#778998" transform="translate(27,12)" d="M0 0h6l8 6 5 5 4 1-1 3-7-3-5-4V6L4 5z"/><path fill="#3390DF" transform="translate(26,21)" d="M0 0l4 2-15 15-1-3 7-10z"/><path fill="#3FA1B7" transform="translate(27,18)" d="M0 0l2 1-6 4-5 4-4 4-1-3 1-3 7-3 3-3z"/><path fill="#8277BB" transform="translate(37,18)" d="M0 0h4l5 5 4 1-1 3-7-3-5-4z"/><path fill="#4989CF" transform="translate(30,17)" d="M0 0l5 1 2 5-9-1z"/><path fill="#71B774" transform="translate(23,12)" d="M0 0l5 1-3 2-5 3-4 1 1-4z"/><path fill="#6687E9" transform="translate(44,28)" d="M0 0l7 1 2 6-4-1-5-5z"/><path fill="#C7AF38" transform="translate(23,3)" d="M0 0h7l-2 1v2l3 1-4 1-6-1z"/><path fill="#EF842A" transform="translate(28,0)" d="M0 0h8v3L4 4l-8-1z"/><path fill="#F35241" transform="translate(36,1)" d="M0 0l5 2 4 6-1 3-6-8-2-1z"/></svg>`,
  codex: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="#5965e8" d="M9.064 3.344a4.578 4.578 0 012.285-.312c1 .115 1.891.54 2.673 1.275a.09.09 0 00.08.021 4.55 4.55 0 013.046.275l.163.079a4.581 4.581 0 012.188 2.399c.209.51.313 1.041.315 1.595a4.24 4.24 0 01-.134 1.223.123.123 0 00.03.115c.594.607.988 1.33 1.183 2.17.289 1.425-.007 2.71-.887 3.854l-.136.166a4.548 4.548 0 01-2.201 1.388.123.123 0 00-.081.076c-.191.551-.383 1.023-.74 1.494-.9 1.187-2.222 1.846-3.711 1.838-1.187-.006-2.239-.44-3.157-1.302a.107.107 0 00-.105-.024c-.388.125-.78.143-1.204.138a4.441 4.441 0 01-1.945-.466 4.544 4.544 0 01-1.61-1.335c-.152-.202-.303-.392-.414-.617a5.81 5.81 0 01-.37-.961 4.582 4.582 0 01-.014-2.298.124.124 0 00-.021-.104 4.467 4.467 0 01-1.034-1.651 3.896 3.896 0 01-.251-1.192 5.189 5.189 0 01.141-1.6c.337-1.112.982-1.985 1.933-2.618.212-.141.413-.251.601-.33.215-.089.43-.164.646-.227a.098.098 0 00.065-.066 4.51 4.51 0 01.829-1.615 4.535 4.535 0 011.837-1.388zm3.482 10.565a.637.637 0 000 1.272h3.636a.637.637 0 100-1.272h-3.636zM8.462 9.23a.637.637 0 00-1.106.631l1.272 2.224-1.266 2.136a.636.636 0 101.095.649l1.454-2.455a.636.636 0 00.005-.64L8.462 9.23z"/></svg>`,
  gemini: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="#3186FF" d="M20.616 10.835a14.147 14.147 0 01-4.45-3.001 14.111 14.111 0 01-3.678-6.452.503.503 0 00-.975 0 14.134 14.134 0 01-3.679 6.452 14.155 14.155 0 01-4.45 3.001c-.65.28-1.318.505-2.002.678a.502.502 0 000 .975c.684.172 1.35.397 2.002.677a14.147 14.147 0 014.45 3.001 14.112 14.112 0 013.679 6.453.502.502 0 00.975 0c.172-.685.397-1.351.677-2.003a14.145 14.145 0 013.001-4.45 14.113 14.113 0 016.453-3.678.503.503 0 000-.975 13.245 13.245 0 01-2.003-.678z"/></svg>`,
  kimi: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="#1783FF" d="M21.846 0a1.923 1.923 0 110 3.846H20.15a.226.226 0 01-.227-.226V1.923C19.923.861 20.784 0 21.846 0z"/><path fill="#fff" d="M11.065 11.199l7.257-7.2c.137-.136.06-.41-.116-.41H14.3a.164.164 0 00-.117.051l-7.82 7.756c-.122.12-.302.013-.302-.179V3.82c0-.127-.083-.23-.185-.23H3.186c-.103 0-.186.103-.186.23V19.77c0 .128.083.23.186.23h2.69c.103 0 .186-.102.186-.23v-3.25c0-.069.025-.135.069-.178l2.424-2.406a.158.158 0 01.205-.023l6.484 4.772a7.677 7.677 0 003.453 1.283c.108.012.2-.095.2-.23v-3.06c0-.117-.07-.212-.164-.227a5.028 5.028 0 01-2.027-.807l-5.613-4.064c-.117-.078-.132-.279-.028-.381z"/></svg>`,
  xai: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M9.27 15.29l7.978-5.897c.391-.29.95-.177 1.137.272.98 2.369.542 5.215-1.41 7.169-1.951 1.954-4.667 2.382-7.149 1.406l-2.711 1.257c3.889 2.661 8.611 2.003 11.562-.953 2.341-2.344 3.066-5.539 2.388-8.42l.006.007c-.983-4.232.242-5.924 2.75-9.383L24 .5l-3.301 3.305v-.01L9.267 15.292M7.623 16.723c-2.792-2.67-2.31-6.801.071-9.184 1.761-1.763 4.647-2.483 7.166-1.425l2.705-1.25a7.808 7.808 0 00-1.829-1A8.975 8.975 0 005.984 5.83c-2.533 2.536-3.33 6.436-1.962 9.764 1.022 2.487-.653 4.246-2.34 6.022-.599.63-1.199 1.259-1.682 1.925l7.62-6.815"/></svg>`,
  vertex: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path d="M20,13.89A.77.77,0,0,0,19,13.73l-7,5.14v.22a.72.72,0,1,1,0,1.43v0a.74.74,0,0,0,.45-.15l7.41-5.47A.76.76,0,0,0,20,13.89Z" fill="#669df6"/><path d="M12,20.52a.72.72,0,0,1,0-1.43h0v-.22L5,13.73a.76.76,0,0,0-1,.16.74.74,0,0,0,.16,1l7.41,5.47a.73.73,0,0,0,.44.15v0Z" fill="#aecbfa"/><path d="M12,18.34a1.47,1.47,0,1,0,1.47,1.47A1.47,1.47,0,0,0,12,18.34Zm0,2.18a.72.72,0,1,1,.72-.71A.71.71,0,0,1,12,20.52Z" fill="#4285f4"/><path d="M6,6.11a.76.76,0,0,1-.75-.75V3.48a.76.76,0,1,1,1.51,0V5.36A.76.76,0,0,1,6,6.11Z" fill="#aecbfa"/><circle cx="5.98" cy="12" r="0.76" fill="#aecbfa"/><circle cx="5.98" cy="9.79" r="0.76" fill="#aecbfa"/><circle cx="5.98" cy="7.57" r="0.76" fill="#aecbfa"/><path d="M18,8.31a.76.76,0,0,1-.75-.76V5.67a.75.75,0,1,1,1.5,0V7.55A.75.75,0,0,1,18,8.31Z" fill="#4285f4"/><circle cx="18.02" cy="12.01" r="0.76" fill="#4285f4"/><circle cx="18.02" cy="9.76" r="0.76" fill="#4285f4"/><circle cx="18.02" cy="3.48" r="0.76" fill="#4285f4"/><path d="M12,15a.76.76,0,0,1-.75-.75V12.34a.76.76,0,0,1,1.51,0v1.89A.76.76,0,0,1,12,15Z" fill="#669df6"/><circle cx="12" cy="16.45" r="0.76" fill="#669df6"/><circle cx="12" cy="10.14" r="0.76" fill="#669df6"/><circle cx="12" cy="7.92" r="0.76" fill="#669df6"/><path d="M15,10.54a.76.76,0,0,1-.75-.75V7.91a.76.76,0,1,1,1.51,0V9.79A.76.76,0,0,1,15,10.54Z" fill="#4285f4"/><circle cx="15.01" cy="5.69" r="0.76" fill="#4285f4"/><circle cx="15.01" cy="14.19" r="0.76" fill="#4285f4"/><circle cx="15.01" cy="11.97" r="0.76" fill="#4285f4"/><circle cx="8.99" cy="14.19" r="0.76" fill="#aecbfa"/><circle cx="8.99" cy="7.92" r="0.76" fill="#aecbfa"/><circle cx="8.99" cy="5.69" r="0.76" fill="#aecbfa"/><path d="M9,12.73A.76.76,0,0,1,8.24,12V10.1a.75.75,0,1,1,1.5,0V12A.75.75,0,0,1,9,12.73Z" fill="#aecbfa"/></svg>`,
  meta: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="#0466E0" d="M6.897 4h-.024l-.031 2.615h.022c1.715 0 3.046 1.357 5.94 6.246l.175.297.012.02 1.62-2.438-.012-.019a48.763 48.763 0 00-1.098-1.716 28.01 28.01 0 00-1.175-1.629C10.413 4.932 8.812 4 6.896 4z"/><path fill="#0064E0" d="M6.873 4C4.95 4.01 3.247 5.258 2.02 7.17a4.352 4.352 0 00-.01.017l2.254 1.231.011-.017c.718-1.083 1.61-1.774 2.568-1.785h.021L6.896 4h-.023z"/><path fill="#006BE6" d="M2.019 7.17l-.011.017C1.2 8.447.598 9.995.274 11.664l-.005.022 2.534.6.004-.022c.27-1.467.786-2.828 1.456-3.845l.011-.017L2.02 7.17z"/><path fill="#0077F1" d="M2.807 12.264l-2.533-.6-.005.022c-.177.918-.267 1.851-.269 2.786v.023l2.598.233v-.023a12.591 12.591 0 01.21-2.44z"/><path fill="#007EF8" d="M2.677 15.537a5.462 5.462 0 01-.079-.813v-.022L0 14.468v.024a8.89 8.89 0 00.146 1.652l2.535-.585a4.106 4.106 0 01-.004-.022z"/><path fill="#0080FA" d="M3.27 16.89c-.284-.31-.484-.756-.589-1.328l-.004-.021-2.535.585.004.021c.192 1.01.568 1.85 1.106 2.487l.014.017 2.018-1.745a2.106 2.106 0 01-.015-.016z"/><path fill="#0082FB" d="M10.78 9.654c-1.528 2.35-2.454 3.825-2.454 3.825-2.035 3.2-2.739 3.917-3.871 3.917a1.545 1.545 0 01-1.186-.508l-2.017 1.744.014.017C2.01 19.518 3.058 20 4.356 20c1.963 0 3.374-.928 5.884-5.33l1.766-3.13a41.283 41.283 0 00-1.227-1.886z"/><path fill="#0080FA" d="M13.502 5.946l-.016.016c-.4.43-.786.908-1.16 1.416.378.483.768 1.024 1.175 1.63.48-.743.928-1.345 1.367-1.807l.016-.016-1.382-1.24z"/><path fill="#0082FB" d="M20.918 5.713C19.853 4.633 18.583 4 17.225 4c-1.432 0-2.637.787-3.723 1.944l-.016.016 1.382 1.24.016-.017c.715-.747 1.408-1.12 2.176-1.12.826 0 1.6.39 2.27 1.075l.015.016 1.589-1.425-.016-.016z"/><path fill="#0082FA" d="M23.998 14.125c-.06-3.467-1.27-6.566-3.064-8.396l-.016-.016-1.588 1.424.015.016c1.35 1.392 2.277 3.98 2.361 6.971v.023h2.292v-.022z"/><path fill="#0080FA" d="M23.998 14.15v-.023h-2.292v.022c.004.14.006.282.006.424 0 .815-.121 1.474-.368 1.95l-.011.022 1.708 1.782.013-.02c.62-.96.946-2.293.946-3.91 0-.083 0-.165-.002-.247z"/><path fill="#017DF6" d="M21.344 16.52l-.011.02c-.214.402-.519.67-.917.787l.778 2.462a3.493 3.493 0 00.438-.182 3.558 3.558 0 001.366-1.218l.044-.065.012-.02-1.71-1.784z"/><path fill="#0278F0" d="M19.92 17.393c-.262 0-.492-.039-.718-.14l-.798 2.522c.449.153.927.222 1.46.222.492 0 .943-.073 1.352-.215l-.78-2.462c-.167.05-.341.075-.517.073z"/><path fill="#0474EC" d="M18.323 16.534l-.014-.017-1.836 1.914.016.017c.637.682 1.246 1.105 1.937 1.337l.797-2.52c-.291-.125-.573-.353-.9-.731z"/><path fill="#066CE4" d="M18.309 16.515c-.55-.642-1.232-1.712-2.303-3.44l-1.396-2.336-.011-.02-1.62 2.438.012.02.989 1.668c.959 1.61 1.74 2.774 2.493 3.585l.016.016 1.834-1.914a2.353 2.353 0 01-.014-.017z"/></svg>`,
  openai: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" fill-rule="evenodd" d="M21.55 10.004a5.416 5.416 0 00-.478-4.501c-1.217-2.09-3.662-3.166-6.05-2.66A5.59 5.59 0 0010.831 1C8.39.995 6.224 2.546 5.473 4.838A5.553 5.553 0 001.76 7.496a5.487 5.487 0 00.691 6.5 5.416 5.416 0 00.477 4.502c1.217 2.09 3.662 3.165 6.05 2.66A5.586 5.586 0 0013.168 23c2.443.006 4.61-1.546 5.361-3.84a5.553 5.553 0 003.715-2.66 5.488 5.488 0 00-.693-6.497v.001zm-8.381 11.558a4.199 4.199 0 01-2.675-.954c.034-.018.093-.05.132-.074l4.44-2.53a.71.71 0 00.364-.623v-6.176l1.877 1.069c.02.01.033.029.036.05v5.115c-.003 2.274-1.87 4.118-4.174 4.123zM4.192 17.78a4.059 4.059 0 01-.498-2.763c.032.02.09.055.131.078l4.44 2.53c.225.13.504.13.73 0l5.42-3.088v2.138a.068.068 0 01-.027.057L9.9 19.288c-1.999 1.136-4.552.46-5.707-1.51h-.001zM3.023 8.216A4.15 4.15 0 015.198 6.41l-.002.151v5.06a.711.711 0 00.364.624l5.42 3.087-1.876 1.07a.067.067 0 01-.063.005l-4.489-2.559c-1.995-1.14-2.679-3.658-1.53-5.63h.001zm15.417 3.54l-5.42-3.088L14.896 7.6a.067.067 0 01.063-.006l4.489 2.557c1.998 1.14 2.683 3.662 1.529 5.633a4.163 4.163 0 01-2.174 1.807V12.38a.71.71 0 00-.363-.623zm1.867-2.773a6.04 6.04 0 00-.132-.078l-4.44-2.53a.731.731 0 00-.729 0l-5.42 3.088V7.325a.068.068 0 01.027-.057L14.1 4.713c2-1.137 4.555-.46 5.707 1.513.487.833.664 1.809.499 2.757h.001zm-11.741 3.81l-1.877-1.068a.065.065 0 01-.036-.051V6.559c.001-2.277 1.873-4.122 4.181-4.12.976 0 1.92.338 2.671.954-.034.018-.092.05-.131.073l-4.44 2.53a.71.71 0 00-.365.623l-.003 6.173v.002zm1.02-2.168L12 9.25l2.414 1.375v2.75L12 14.75l-2.415-1.375v-2.75z"/></svg>`
};

function providerIcon(brand) {
  const markup = PROVIDER_ICONS[brand];
  if (!markup) return null;
  return el("span", { class: "provider-icon", "data-provider": brand, role: "img", "aria-label": PROVIDER_LABELS[brand] }, iconSVG(markup));
}

function credentialUsageSummary({ requests, failed, input_tokens: inputTokens, output_tokens: outputTokens }) {
  const succeeded = requests - failed;
  const digits = failed === 0 || succeeded === 0 ? 0 : 2;
  return {
    requests,
    succeeded,
    failed,
    successRate: requests ? ((succeeded / requests) * 100).toFixed(digits) + "%" : "—",
    totalTokens: inputTokens + outputTokens
  };
}

function credentialMetricHeaders() { return ["ui.in_flight_requests", "ui.historical_usage"].map((key) => m(key)); }

function credentialMetric(name, label, value) {
  return el(
    "td",
    { class: "credential-" + name + "-cell" },
    el(
      "div",
      { class: "credential-metric" },
      el("span", { class: "credential-metric-label", text: label }),
      el("span", { class: "credential-metric-value" }, value)
    )
  );
}

function renderCredentialMetrics({ usage, in_flight: inFlight }, breakdown) {
  return [credentialMetric("inflight", m("ui.in_flight_requests"), int(inFlight)), renderCredentialUsage(usage, breakdown)];
}

function renderCredentialUsage(usage, breakdown) {
  const summary = usage && credentialUsageSummary(usage);
  const requests = [summary ? tokens(summary.succeeded, 2) : "—"];
  if (summary?.failed > 0) {
    requests.push(DISPLAY_SLASH, el("span", { class: "credential-request-failed", text: tokens(summary.failed, 2) }));
  }
  const metrics = [
    ["arrow-left-right", m("ui.successful_failed_requests"), requests],
    ["square-text", m("ui.auth_total_tokens"), summary ? tokens(summary.totalTokens) : "—"],
    ["circle-check", m("ui.success_rate"), summary ? summary.successRate : "—"],
    ["circle-dollar-sign", m("ui.cost"), usage ? usageMoney(usage.cost_usd, usage.charged_in, 2) : "—"]
  ];
  return el(
    "td",
    { class: "credential-usage-cell", ...hoverTooltipProps(breakdown, breakdown && summary?.requests > 0) },
    el("span", { class: "credential-metric-label", text: m("ui.historical_usage") }),
    el(
      "div",
      { class: "credential-usage" },
      metrics.map(([icon, label, value]) =>
        el(
          "span",
          { class: "credential-usage-item" },
          actionIcon(icon),
          el("span", { class: "visually-hidden" }, label, ": "),
          el("span", {}, value)
        )
      )
    )
  );
}
