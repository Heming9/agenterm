package main

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"io/fs"
	"net/http"
	"net/url"
)

func parsePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, i18n.NewError("public.url.must.be.an.http.s.origin", protocol.PathPrefix)
	}
	switch u.Path {
	case "", "/", protocol.PathPrefix, protocol.PathPrefix + "/":
		u.Path = protocol.PathPrefix
	default:
		return nil, i18n.NewError("public.url.path.must.be.s", protocol.PathPrefix)
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
	mux.Handle(protocol.PathPrefix+"/", http.StripPrefix(protocol.PathPrefix, routes))
	return mux
}
