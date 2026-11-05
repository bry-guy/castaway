## Castaway Season {{.Season}}: Week {{.Week}} Scores

{{if .Boots}}{{.Boots}}

{{end}}👑 {{.Leader}}
{{- if .Gainer}}
📈 {{.Gainer}}{{end}}
{{- if .Slider}}
📉 {{.Slider}}{{end}}

{{range .All}}{{if eq .Place 1}}🥇{{else if eq .Place 2}}🥈{{else if eq .Place 3}}🥉{{else}}{{.Place}}.{{end}} {{.Mention}}: {{.Total}}
{{end -}}
{{- if .GameScore}}
{{.GameScore}}{{end}}

Full scores: <{{.Site}}>
{{- if .NextGame}}

{{.NextGame}}{{end}}
