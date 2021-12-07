package adapt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
)

// Provider wraps the provider implementation as a Caddy module.
type adminAdapt struct{}

func init() {
	caddy.RegisterModule(adminAdapt{})
}

// CaddyModule returns the Caddy module information.
func (adminAdapt) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "admin.api.adapt",
		New: func() caddy.Module { return new(adminAdapt) },
	}
}

// Routes returns a route for the /load endpoint.
func (al adminAdapt) Routes() []caddy.AdminRoute {
	return []caddy.AdminRoute{
		{
			Pattern: "/adapt",
			Handler: caddy.AdminHandlerFunc(al.handleAdapt),
		},
	}
}

// handleLoad replaces the entire current configuration with
// a new one provided in the response body. It supports config
// adapters through the use of the Content-Type header. A
// config that is identical to the currently-running config
// will be a no-op unless Cache-Control: must-revalidate is set.
func (adminAdapt) handleAdapt(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return caddy.APIError{
			HTTPStatus: http.StatusMethodNotAllowed,
			Err:        fmt.Errorf("method not allowed"),
		}
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	_, err := io.Copy(buf, r.Body)
	if err != nil {
		return caddy.APIError{
			HTTPStatus: http.StatusBadRequest,
			Err:        fmt.Errorf("reading request body: %v", err),
		}
	}
	body := buf.Bytes()

	// if the config is formatted other than Caddy's native
	// JSON, we need to adapt it before loading it
	if ctHeader := r.Header.Get("Content-Type"); ctHeader != "" {
		result, warnings, err := adaptByContentType(ctHeader, body)
		if err != nil {
			return caddy.APIError{
				HTTPStatus: http.StatusBadRequest,
				Err:        err,
			}
		}
		if len(warnings) > 0 {
			_, err := json.Marshal(warnings)
			if err != nil {
				caddy.Log().Named("admin.api.load").Error(err.Error())
			}
		}
		body = result
	}

	w.Header().Add("Content-Type", "application/json")
	w.Write(body)

	return nil
}

// adaptByContentType adapts body to Caddy JSON using the adapter specified by contenType.
// If contentType is empty or ends with "/json", the input will be returned, as a no-op.
func adaptByContentType(contentType string, body []byte) ([]byte, []caddyconfig.Warning, error) {
	// assume JSON as the default
	if contentType == "" {
		return body, nil, nil
	}

	ct, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, nil, caddy.APIError{
			HTTPStatus: http.StatusBadRequest,
			Err:        fmt.Errorf("invalid Content-Type: %v", err),
		}
	}

	// if already JSON, no need to adapt
	if strings.HasSuffix(ct, "/json") {
		return body, nil, nil
	}

	// adapter name should be suffix of MIME type
	slashIdx := strings.Index(ct, "/")
	if slashIdx < 0 {
		return nil, nil, fmt.Errorf("malformed Content-Type")
	}

	adapterName := ct[slashIdx+1:]
	cfgAdapter := caddyconfig.GetAdapter(adapterName)
	if cfgAdapter == nil {
		return nil, nil, fmt.Errorf("unrecognized config adapter '%s'", adapterName)
	}

	result, warnings, err := cfgAdapter.Adapt(body, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("adapting config using %s adapter: %v", adapterName, err)
	}

	return result, warnings, nil
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}
