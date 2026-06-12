// Package web serves the read-only HTML board — the human inspection layer
// over HTTP. It is a thin consumer of store, rendering a single template with
// no JavaScript.
package web

import (
	"html/template"
	"net/http"
	"time"

	"github.com/colossus21/ticketd/internal/domain"
	"github.com/colossus21/ticketd/internal/store"
)

// columns is the left-to-right ordering of status lanes on the board.
var columns = []domain.Status{
	domain.Backlog, domain.Todo, domain.InProgress,
	domain.InReview, domain.Blocked, domain.Done, domain.WontDo,
}

type column struct {
	Status  domain.Status
	Tickets []domain.Ticket
}

type boardData struct {
	Project   string
	Columns   []column
	Total     int
	Generated string
}

// BoardHandler renders all tickets grouped by status. An optional ?project=
// query filters to one project.
func BoardHandler(st *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		tickets, err := st.AllTickets(r.Context(), project)
		if err != nil {
			http.Error(w, "error loading board: "+err.Error(), http.StatusInternalServerError)
			return
		}

		byStatus := map[domain.Status][]domain.Ticket{}
		for _, t := range tickets {
			byStatus[t.Status] = append(byStatus[t.Status], t)
		}
		data := boardData{
			Project:   project,
			Total:     len(tickets),
			Generated: time.Now().Local().Format("2006-01-02 15:04"),
		}
		if data.Project == "" {
			data.Project = "all projects"
		}
		for _, s := range columns {
			data.Columns = append(data.Columns, column{Status: s, Tickets: byStatus[s]})
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := boardTmpl.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

var boardTmpl = template.Must(template.New("board").Funcs(template.FuncMap{
	"prio": func(p domain.Priority) string { return p.String() },
}).Parse(boardHTML))

const boardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ticketd — {{.Project}}</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.4 system-ui, sans-serif; margin: 0; background: Canvas; color: CanvasText; }
  header { padding: 12px 16px; border-bottom: 1px solid #8884; display: flex; gap: 12px; align-items: baseline; }
  header h1 { font-size: 16px; margin: 0; }
  header .meta { color: #8889; font-size: 12px; }
  .board { display: flex; gap: 12px; padding: 16px; overflow-x: auto; align-items: flex-start; }
  .col { flex: 0 0 260px; background: #8881; border-radius: 8px; padding: 8px; }
  .col h2 { font-size: 12px; text-transform: uppercase; letter-spacing: .04em; margin: 4px 6px 10px; color: #8889; }
  .col h2 .n { color: CanvasText; }
  .card { background: Canvas; border: 1px solid #8883; border-radius: 6px; padding: 8px 10px; margin-bottom: 8px; }
  .card .key { font-weight: 600; font-size: 12px; color: #8889; }
  .card .title { margin: 2px 0 6px; }
  .tags { display: flex; flex-wrap: wrap; gap: 4px; font-size: 11px; color: #8889; }
  .tag { background: #8882; border-radius: 4px; padding: 1px 5px; }
  .p-critical { border-left: 3px solid #e5484d; }
  .p-high { border-left: 3px solid #f5a623; }
  .p-normal { border-left: 3px solid #8884; }
  .p-low { border-left: 3px solid #8882; }
  .empty { color: #8887; font-size: 12px; padding: 4px 6px; }
</style>
</head>
<body>
<header>
  <h1>ticketd</h1>
  <span class="meta">{{.Project}} · {{.Total}} tickets · generated {{.Generated}} · read-only</span>
</header>
<div class="board">
{{range .Columns}}
  <section class="col">
    <h2>{{.Status}} <span class="n">{{len .Tickets}}</span></h2>
    {{- if not .Tickets}}<div class="empty">—</div>{{end}}
    {{- range .Tickets}}
    <article class="card p-{{prio .Priority}}">
      <div class="key">{{.Key}}{{if .ParentKey}} · ↳ {{.ParentKey}}{{end}}</div>
      <div class="title">{{.Title}}</div>
      <div class="tags">
        <span class="tag">{{prio .Priority}}</span>
        {{- if .CommentCount}}<span class="tag">{{.CommentCount}} 💬</span>{{end}}
        {{- range .Labels}}<span class="tag">{{.}}</span>{{end}}
      </div>
    </article>
    {{- end}}
  </section>
{{end}}
</div>
</body>
</html>
`
