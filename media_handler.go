package main

import (
	"net/http"
	"path/filepath"
	"strings"
)

type mediaHandler struct {
	root     string
	fallback http.Handler
}

func (h *mediaHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(request.URL.Path, "/media/") {
		h.fallback.ServeHTTP(response, request)
		return
	}
	relative := strings.TrimPrefix(request.URL.Path, "/media/")
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	if cleaned == "." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		http.NotFound(response, request)
		return
	}
	path := filepath.Join(h.root, cleaned)
	resolvedRoot, rootErr := filepath.Abs(h.root)
	resolvedPath, pathErr := filepath.Abs(path)
	if rootErr != nil || pathErr != nil || !strings.HasPrefix(strings.ToLower(resolvedPath), strings.ToLower(resolvedRoot+string(filepath.Separator))) {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(response, request, resolvedPath)
}
