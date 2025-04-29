// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

// ConfigProvider defines an interface for retrieving configuration data.
type ConfigProvider interface {
	GetConfig() (string, error)
}

// FileConfigProvider implements ConfigProvider by reading configuration from a file.
type FileConfigProvider struct {
	Path string
}

// GetConfig reads the configuration file and returns its contents as a string.
func (f *FileConfigProvider) GetConfig() (string, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return "", fmt.Errorf("error reading config file %s: %w", f.Path, err)
	}
	return string(data), nil
}

// Server represents an HTTP server with a dependency on a ConfigProvider.
type Server struct {
	ConfigProvider ConfigProvider
}

// ServeHTTP routes incoming HTTP requests to the appropriate handler methods.
// It implements the http.Handler interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/configmap":
		s.handleConfigmap(w)
	case "/amiga/health":
		s.handleHealth(w)
	default:
		http.Error(w, "Unknown path!", http.StatusNotFound)
	}
}

// handleConfigmap handles requests to the /configmap endpoint.
// It sets a YAML-related Content-Type and returns the raw config as text.
// If reading the config fails, it returns a 404 with an error message.
func (s *Server) handleConfigmap(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")

	config, err := s.ConfigProvider.GetConfig()
	if err != nil {
		http.Error(w, "File not found (or error reading it)!", http.StatusNotFound)
		log.Printf("Error reading configmap file: %v", err)
		return
	}

	fmt.Fprint(w, config)
}

// handleHealth handles requests to the /amiga/health endpoint.
// It sets a plain text Content-Type and responds with a basic "ok" status.
func (s *Server) handleHealth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}
