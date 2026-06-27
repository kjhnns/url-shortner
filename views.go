package main

import (
	"html/template"
	"net/http"
)

// templates is the single parsed template set, shared by the auth layer (login)
// and the request handlers. Each file defines a full page via {{define}}.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

func renderPage(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderLogin shows the password login form.
func renderLogin(w http.ResponseWriter, next, errMsg string) {
	renderPage(w, "login.html", map[string]any{"Next": next, "Error": errMsg})
}
