package challenge

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
)

// Info is what the seed page shows about the server. It is a function so the page always reflects the current state.
type Info struct {
	Mode        string   // where random levels come from, in words
	Fingerprint string   // fingerprint of the whole-archive index; "" when there is none
	Players     []string // NNIDs that have logged in, for the "who" list
}

var page = template.Must(template.New("seed").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Challenge seed</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:44em;margin:2em auto;padding:0 1em;color:#222}
 h1{margin-bottom:.2em} table{border-collapse:collapse;width:100%} th,td{text-align:left;padding:.35em .6em;border-bottom:1px solid #ddd}
 code{background:#f1f1f1;padding:.1em .35em;border-radius:3px} input,select,button{font:inherit;padding:.3em .5em}
 .note{color:#555} .err{color:#b00020;font-weight:600}
</style></head><body>
<h1>Challenge seed</h1>
<p>With a seed set, the "random" levels of a list are not random any more: the same seed always gives the same levels,
so friends who enter the same seed play the same challenge. Without a seed, levels are random as usual.</p>
<p><b>Levels come from:</b> {{.Info.Mode}}</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<h2>Active seeds</h2>
{{if .Entries}}<table><tr><th>Who</th><th>Seed</th><th>Challenge code</th><th></th></tr>
{{range .Entries}}<tr><td>{{.Who}}</td><td>{{.Seed}}</td><td><code>{{.Code}}</code></td>
<td><form method="post" action="/seed"><input type="hidden" name="who" value="{{.Scope}}"><button name="action" value="clear">Clear</button></form></td></tr>
{{end}}</table>
<p class="note">A player's own seed wins over the one for everyone. Players on different servers get the same levels only if
their challenge codes match (same seed, same level archive).</p>
{{else}}<p>None: levels are random.</p>{{end}}
<h2>Set a seed</h2>
<form method="post" action="/seed">
 <p><label>Who: <select name="who"><option value="*">Everyone</option>
 {{range .Info.Players}}<option value="nnid:{{.}}">{{.}}</option>{{end}}</select></label></p>
 <p><label>Seed: <input name="seed" maxlength="64" size="30" placeholder="any words, e.g. friday race"></label>
 <button name="action" value="set">Set seed</button>
 <button name="action" value="random">Pick a random seed</button></p>
</form>
<p class="note">Case and extra spaces do not matter. Starting a new round with fresh levels: just change the seed.
There is no password: anyone on your network can change this page.</p>
</body></html>`))

type view struct {
	Info    Info
	Entries []viewEntry
	Error   string
}

type viewEntry struct{ Scope, Who, Seed, Code string }

// Handler serves GET /seed (the page), POST /seed (set / random / clear) and GET /seed.json.
func (s *Store) Handler(info func() Info) http.Handler {
	entries := func(fp string) ([]viewEntry, error) {
		list, err := s.List()
		if err != nil {
			return nil, err
		}

		out := make([]viewEntry, 0, len(list))

		for _, e := range list {
			who := "Everyone"
			if n, ok := strings.CutPrefix(e.Scope, "nnid:"); ok {
				who = n
			}

			out = append(out, viewEntry{Scope: e.Scope, Who: who, Seed: e.Seed, Code: Code(e.Seed, orNone(fp))})
		}

		return out, nil
	}

	render := func(w http.ResponseWriter, status int, msg string) {
		inf := info()

		es, err := entries(inf.Fingerprint)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		page.Execute(w, view{Info: inf, Entries: es, Error: msg}) //nolint:errcheck
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/seed", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			render(w, http.StatusOK, "")
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 4096)

			if err := r.ParseForm(); err != nil {
				render(w, http.StatusBadRequest, "That request could not be read.")
				return
			}

			who := r.PostForm.Get("who")
			if who == "" {
				who = Everyone
			}

			var err error

			switch r.PostForm.Get("action") {
			case "set":
				err = s.Set(who, r.PostForm.Get("seed"))
			case "random":
				err = s.Set(who, RandomSeed())
			case "clear":
				err = s.Clear(who)
			default:
				render(w, http.StatusBadRequest, "Unknown action.")
				return
			}

			if err != nil {
				render(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "challenge: "))
				return
			}

			http.Redirect(w, r, "/seed", http.StatusSeeOther)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/seed.json", func(w http.ResponseWriter, r *http.Request) {
		inf := info()

		es, err := entries(inf.Fingerprint)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		type jsonEntry struct {
			Scope string `json:"scope"`
			Seed  string `json:"seed"`
			Code  string `json:"code"`
		}

		out := struct {
			Mode        string      `json:"mode"`
			Fingerprint string      `json:"fingerprint"`
			Seeds       []jsonEntry `json:"seeds"`
		}{Mode: inf.Mode, Fingerprint: inf.Fingerprint, Seeds: []jsonEntry{}}

		for _, e := range es {
			out.Seeds = append(out.Seeds, jsonEntry{Scope: e.Scope, Seed: e.Seed, Code: e.Code})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out) //nolint:errcheck
	})

	return mux
}

func orNone(fp string) string {
	if fp == "" {
		return "pool"
	}

	return fp
}
