// Package openapi serves the OpenAPI spec and Swagger UI.
// The spec is embedded at build time — no runtime file reads.
package openapi

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed openapi.yaml
var specYAML []byte

// Handler returns an http.ServeMux that serves:
//
//	GET /docs/         → Swagger UI (HTML)
//	GET /docs/openapi.yaml → Raw OpenAPI spec
//
// Mount with: mux.Handle("/docs/", openapi.Handler())
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/docs/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write(specYAML)
	})
	mux.HandleFunc("/docs/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerHTML))
	})
	return mux
}

// swaggerHTML is the self-contained Swagger UI page.
// Uses Swagger UI CDN — no build dependency on the swagger-ui npm package.
const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
  <title>One Fintech Card & Member API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"/>
  <style>
    body { margin: 0; background: #fafafa; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; }
    .topbar { background: #1a2e4a !important; }
    .topbar-wrapper .link span { display: none; }
    .topbar-wrapper .link::after {
      content: 'One Fintech Platform';
      color: white;
      font-size: 18px;
      font-weight: 600;
      letter-spacing: -0.3px;
    }
    .info .title { color: #1a2e4a !important; }
    .swagger-ui .btn.authorize { background: #1a2e4a; border-color: #1a2e4a; color: white; }
    .swagger-ui .btn.authorize svg { fill: white; }
    /* Mock mode banner */
    #mock-banner {
      background: #fff3cd;
      border-bottom: 2px solid #ffc107;
      padding: 10px 20px;
      font-size: 14px;
      display: flex;
      align-items: center;
      gap: 10px;
    }
    #mock-banner strong { color: #856404; }
    #mock-banner code {
      background: #f8f9fa;
      padding: 2px 6px;
      border-radius: 3px;
      font-size: 12px;
      border: 1px solid #dee2e6;
    }
    #dev-claims-form {
      background: #e8f4f8;
      border: 1px solid #b8daff;
      border-radius: 6px;
      padding: 16px 20px;
      margin: 12px 20px;
      font-size: 14px;
    }
    #dev-claims-form h4 { margin: 0 0 10px; color: #004085; font-size: 14px; }
    #dev-claims-form label { display: block; margin-bottom: 6px; color: #333; }
    #dev-claims-form input, #dev-claims-form select {
      padding: 4px 8px; border: 1px solid #ccc; border-radius: 4px;
      font-size: 13px; margin-left: 8px;
    }
    #dev-claims-form button {
      margin-top: 10px; padding: 6px 14px; background: #1a2e4a;
      color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 13px;
    }
    #claims-preview {
      margin-top: 8px; font-size: 12px; color: #555;
      font-family: monospace; background: #f8f9fa; padding: 6px; border-radius: 4px;
    }
  </style>
</head>
<body>
  <div id="mock-banner">
    <span>⚠️</span>
    <span>
      <strong>Mock mode active.</strong>
      FIS calls are handled by the in-memory mock adapter.
      Use <code>X-Dev-Claims</code> header or the form below for auth.
      Real Cognito JWT auth pending John Stevens credentials.
    </span>
  </div>

  <div id="dev-claims-form">
    <h4>🛠 Dev Claims Builder</h4>
    <label>
      Tenant ID:
      <input id="dc-tenant" type="text" value="rfu-oregon" size="20"/>
    </label>
    <label>
      Caller type:
      <select id="dc-caller">
        <option value="universal_wallet">universal_wallet</option>
        <option value="ivr">ivr</option>
        <option value="dead_letter_repair">dead_letter_repair</option>
        <option value="ops_portal">ops_portal</option>
        <option value="batch_pipeline">batch_pipeline</option>
      </select>
    </label>
    <label>
      Subject member ID (UW only):
      <input id="dc-member" type="text" placeholder="leave blank for service accounts" size="40"/>
    </label>
    <button onclick="applyDevClaims()">Apply to Swagger UI</button>
    <div id="claims-preview"></div>
  </div>

  <div id="swagger-ui"></div>

  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
  <script>
    // Build and apply X-Dev-Claims as an apiKey
    function buildClaims() {
      const claims = {
        tenant_id: document.getElementById('dc-tenant').value,
        caller_type: document.getElementById('dc-caller').value,
      };
      const memberID = document.getElementById('dc-member').value.trim();
      if (memberID) claims.subject_member_id = memberID;
      return JSON.stringify(claims);
    }

    function applyDevClaims() {
      const val = buildClaims();
      document.getElementById('claims-preview').textContent = 'X-Dev-Claims: ' + val;
      if (window.ui) {
        window.ui.preauthorize("DevClaims", val);
      }
    }

    // UUID generator that works over HTTP (no secure context required).
    // Falls back to Math.random when crypto.randomUUID is unavailable.
    function generateUUID() {
      if (window.crypto && window.crypto.randomUUID) {
        return window.crypto.randomUUID();
      }
      return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
        var r = Math.random() * 16 | 0;
        return (c === 'x' ? r : (r & 0x3 | 0x8)).toString(16);
      });
    }

    window.onload = function() {
      window.ui = SwaggerUIBundle({
        url: "/docs/openapi.yaml",
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset,
        ],
        plugins: [SwaggerUIBundle.plugins.DownloadUrl],
        layout: "StandaloneLayout",
        persistAuthorization: true,
        requestInterceptor: function(req) {
          // Auto-inject X-Dev-Claims if set
          const preview = document.getElementById('claims-preview').textContent;
          if (preview.startsWith('X-Dev-Claims: ') && !req.headers['Authorization']) {
            req.headers['X-Dev-Claims'] = preview.replace('X-Dev-Claims: ', '');
          }
          // Auto-generate X-Correlation-ID if not present
          if (!req.headers['X-Correlation-ID']) {
            req.headers['X-Correlation-ID'] = generateUUID();
          }
          return req;
        },
        onComplete: function() {
          // Auto-apply dev claims on load
          applyDevClaims();
        }
      });
    };
  </script>
</body>
</html>`

// SpecYAML returns the raw OpenAPI YAML bytes for use in tests or programmatic access.
func SpecYAML() []byte {
	cp := make([]byte, len(specYAML))
	copy(cp, specYAML)
	return cp
}

// SpecSummary returns a one-line summary for health check endpoints.
func SpecSummary() string {
	lines := strings.Split(string(specYAML), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "  version:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "  version:"))
		}
	}
	return "unknown"
}
