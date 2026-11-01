package main

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

func parsePublicURL(raw, prefix string) (*url.URL, error) {
	prefix, err := protocol.NormalizePathPrefix(prefix)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, i18n.NewError("public.url.must.be.an.http.s.origin", prefix)
	}
	switch u.Path {
	case "", "/", prefix, prefix + "/":
		u.Path = prefix
	default:
		return nil, i18n.NewError("public.url.path.must.be.s", prefix)
	}
	return u, nil
}

func (s *server) routes(tag string, assets fs.FS) http.Handler {
	routes := http.NewServeMux()
	routes.HandleFunc("/client", s.connect)
	routes.HandleFunc("/invite/redeem", s.redeemInvitation)
	routes.HandleFunc("/mcp", s.mcp)
	routes.Handle("/downloads/", downloadHandler(tag, assets))
	routes.Handle("/c/", s.installWrapper(tag))

	mux := http.NewServeMux()
	prefix := protocol.PathPrefix
	if s.clientURL != "" {
		u, _ := url.Parse(s.clientURL) // Validated when constructing the server/token.
		prefix = strings.TrimSuffix(u.Path, "/client")
	}
	mux.Handle(prefix+"/", http.StripPrefix(prefix, routes))
	return mux
}
