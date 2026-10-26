const getCookie = (key) => {
    const cookie = document.cookie.split('; ').find((c) => c.startsWith(key + '='));
    return cookie && decodeURIComponent(cookie.slice(key.length + 1));
};

const apiKey = (host, id, key) => ({ host, credentials: { type: 'api-key', payload: key, authEntity: id } });

export function getHostAndCredentials() {
    // Set by the module's local web server.
    const host = getCookie('host');
    const id = getCookie('api-key-id');
    const key = getCookie('api-key');
    if (host && id && key) return apiKey(host, id, key);

    // Set by viamapplications.com under /machine/<cookie-key>/.
    const parts = window.location.pathname.split('/');
    if (parts[1] === 'machine' && parts[2]) {
        try {
            const parsed = JSON.parse(getCookie(parts[2]) ?? '');
            if (parsed?.hostname && parsed?.apiKey?.id && parsed?.apiKey?.key) {
                return apiKey(parsed.hostname, parsed.apiKey.id, parsed.apiKey.key);
            }
        } catch {
            // Missing or invalid cookie data
        }
    }

    return apiKey('', '', '');
}
