#!/usr/bin/env node
const http = require('http');
const https = require('https');
const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const os = require('os');
const { URL } = require('url');

const VAULT = process.env.VAULT || 'romaine-kv';
const KV_SECRET_NAME = 'gemini-credentials';
const ESO_NAMESPACE = process.env.ESO_NAMESPACE || 'tank-operator';
const ESO_NAME = process.env.ESO_NAME || 'gemini-credentials';
const PORT = 8085;
const REDIRECT_URI = `http://localhost:${PORT}`;

function requireCmd(cmd) {
  try {
    execSync(`where ${cmd}`, { stdio: 'ignore' });
  } catch {
    try {
      execSync(`which ${cmd}`, { stdio: 'ignore' });
    } catch {
      console.error(`error: '${cmd}' is required but not on PATH`);
      process.exit(1);
    }
  }
}

requireCmd('az');
requireCmd('kubectl');

console.log('Fetching Google OAuth client configuration from Kubernetes secret...');
let clientId, clientSecret;
try {
  const secretJson = execSync(
    `kubectl get secret gemini-api-proxy-config -n ${ESO_NAMESPACE} -o json`,
    { encoding: 'utf-8' }
  );
  const secret = JSON.parse(secretJson);
  clientId = Buffer.from(secret.data.GEMINI_CLIENT_ID, 'base64').toString('utf-8').trim();
  clientSecret = Buffer.from(secret.data.GEMINI_CLIENT_SECRET, 'base64').toString('utf-8').trim();
} catch (err) {
  console.error('Failed to retrieve client configuration from Kubernetes:', err.message);
  console.log('Please enter them manually.');
  process.exit(1);
}

const scopes = [
  'https://www.googleapis.com/auth/cloud-platform',
  'openid',
  'https://www.googleapis.com/auth/userinfo.profile',
  'https://www.googleapis.com/auth/userinfo.email'
].join(' ');

const authUrl = new URL('https://accounts.google.com/o/oauth2/v2/auth');
authUrl.searchParams.set('client_id', clientId);
authUrl.searchParams.set('redirect_uri', REDIRECT_URI);
authUrl.searchParams.set('response_type', 'code');
authUrl.searchParams.set('scope', scopes);
authUrl.searchParams.set('access_type', 'offline');
authUrl.searchParams.set('prompt', 'consent');

const server = http.createServer((req, res) => {
  const reqUrl = new URL(req.url, `http://${req.headers.host}`);
  
  if (reqUrl.pathname === '/') {
    const code = reqUrl.searchParams.get('code');
    if (!code) {
      // Redirect root requests to Google OAuth
      res.writeHead(302, { Location: authUrl.toString() });
      res.end();
      return;
    }

    res.writeHead(200, { 'Content-Type': 'text/html' });
    res.end(`
      <html>
      <body style="font-family: sans-serif; text-align: center; padding-top: 50px; background-color: #f7f9fa; color: #1a202c;">
        <div style="max-width: 500px; margin: 0 auto; padding: 30px; background: white; border-radius: 8px; box-shadow: 0 4px 6px rgba(0,0,0,0.1);">
          <h1 style="color: #4f46e5; margin-bottom: 20px;">Authentication Successful!</h1>
          <p style="font-size: 16px; line-height: 1.5;">You have successfully authorized the Gemini API Proxy.</p>
          <p style="color: #718096; font-size: 14px;">You can now close this window and return to your terminal.</p>
        </div>
      </body>
      </html>
    `);

    server.close();
    exchangeCodeForTokens(code);
  } else {
    res.writeHead(404);
    res.end();
  }
});

server.listen(PORT, () => {
  console.log('\n========================================================================');
  console.log('Gemini Google OAuth Sign-in Assistant');
  console.log('========================================================================');
  console.log('Opening authorization URL in browser...');
  console.log(authUrl.toString());
  console.log('========================================================================\n');

  try {
    const startCmd = process.platform === 'win32' ? 'start' : process.platform === 'darwin' ? 'open' : 'xdg-open';
    execSync(`${startCmd} "${authUrl.toString()}"`);
  } catch (err) {
    console.log('Could not open browser automatically. Please copy and paste the URL above.');
  }
});

function exchangeCodeForTokens(code) {
  console.log('Exchanging authorization code for tokens...');
  
  const postData = JSON.stringify({
    code,
    client_id: clientId,
    client_secret: clientSecret,
    redirect_uri: REDIRECT_URI,
    grant_type: 'authorization_code'
  });

  const req = https.request('https://oauth2.googleapis.com/token', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Content-Length': Buffer.byteLength(postData)
    }
  }, (res) => {
    let data = '';
    res.on('data', (chunk) => data += chunk);
    res.on('end', () => {
      if (res.statusCode !== 200) {
        console.error(`Token exchange failed (status ${res.statusCode}):`, data);
        process.exit(1);
      }
      
      try {
        const tokenResponse = JSON.parse(data);
        saveTokens(tokenResponse);
      } catch (err) {
        console.error('Failed to parse token response:', err.message);
        process.exit(1);
      }
    });
  });

  req.on('error', (err) => {
    console.error('Token exchange request failed:', err.message);
    process.exit(1);
  });

  req.write(postData);
  req.end();
}

function saveTokens(tokenResponse) {
  const oauthCreds = {
    access_token: tokenResponse.access_token,
    refresh_token: tokenResponse.refresh_token,
    scope: tokenResponse.scope,
    token_type: tokenResponse.token_type || 'Bearer',
    id_token: tokenResponse.id_token,
    expiry_date: Date.now() + (tokenResponse.expires_in || 3600) * 1000
  };

  const jsonStr = JSON.stringify(oauthCreds, null, 2);
  const tmpFile = path.join(os.tmpdir(), `gemini-creds-${Date.now()}.json`);
  
  try {
    fs.writeFileSync(tmpFile, jsonStr, 'utf-8');
    
    console.log(`\nWriting Key Vault secret ${VAULT}/${KV_SECRET_NAME}...`);
    execSync(`az keyvault secret set --vault-name "${VAULT}" --name "${KV_SECRET_NAME}" --file "${tmpFile}" --output none`);
    
    console.log(`Forcing ExternalSecret refresh on ${ESO_NAMESPACE}/${ESO_NAME}...`);
    const ts = Math.floor(Date.now() / 1000);
    execSync(`kubectl -n "${ESO_NAMESPACE}" annotate externalsecret "${ESO_NAME}" "force-sync=${ts}" --overwrite`);
    
    console.log('\n✓ Credentials seeded and synchronized successfully!');
    console.log('The gemini-api-proxy will pick up the new credentials containing the correct scopes.');
  } catch (err) {
    console.error('Failed to save credentials:', err.message);
  } finally {
    try {
      if (fs.existsSync(tmpFile)) {
        fs.unlinkSync(tmpFile);
      }
    } catch {}
  }
}
