package httpapi

import "net/http"

// routeRegistrar adds one domain's routes. Domain files register themselves
// from init() so independent work (pipelines, policies, editorial, feature
// stores, ...) never edits the shared route table in New.
type routeRegistrar func(s *Server, mux *http.ServeMux)

var routeRegistrars []routeRegistrar

func registerRoutes(register routeRegistrar) { routeRegistrars = append(routeRegistrars, register) }
