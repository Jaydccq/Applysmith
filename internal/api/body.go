package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

const (
	maxJSONBody   = 262144
	maxLargeBody  = 4 << 20
	maxUploadBody = 10 << 20
)

// readBody enforces the route's body class before the handler runs. For a
// json route it returns the buffered body, so an oversized or mistyped request
// never reaches the handler.
func readBody(w http.ResponseWriter, r *http.Request, class Body) ([]byte, *apikit.Error) {
	switch class {
	case BodyNone:
		if r.ContentLength > 0 {
			return nil, apikit.NewErr(wire.CodeBadRequest, "this route takes no request body")
		}
		if r.ContentLength < 0 {
			var one [1]byte
			if n, _ := r.Body.Read(one[:]); n > 0 {
				return nil, apikit.NewErr(wire.CodeBadRequest, "this route takes no request body")
			}
		}
		return nil, nil
	case BodyJSON:
		if mt := mediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			return nil, apikit.NewErr(wire.CodeUnsupportedMediaType, "the content type must be application/json")
		}
		return capped(w, r, maxJSONBody)
	case BodyJSONLarge:
		if mt := mediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			return nil, apikit.NewErr(wire.CodeUnsupportedMediaType, "the content type must be application/json")
		}
		return capped(w, r, maxLargeBody)
	case BodyModel:
		if mt := mediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			return nil, apikit.NewErr(wire.CodeUnsupportedMediaType, "the content type must be application/json")
		}
		return capped(w, r, agent.MaxModelBody)
	default:
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
		if r.ContentLength > maxUploadBody {
			return nil, apikit.NewErr(wire.CodePayloadTooLarge, "the request body exceeds %d bytes", maxUploadBody)
		}
		return nil, nil
	}
}

func mediaType(h string) string {
	mt, _, _ := strings.Cut(h, ";")
	return strings.ToLower(strings.TrimSpace(mt))
}

func capped(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *apikit.Error) {
	if r.ContentLength > limit {
		return nil, apikit.NewErr(wire.CodePayloadTooLarge, "the request body exceeds %d bytes", limit)
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, apikit.NewErr(wire.CodePayloadTooLarge, "the request body exceeds %d bytes", limit)
	case err != nil:
		return nil, apikit.NewErr(wire.CodeBadRequest, "the request body could not be read")
	}
	return b, nil
}
