## Castaway Season {{.Season}}: Week {{.Week}} Scores

{{if .Boots}}{{.Boots}}

{{end}}👑 {{.Leader}}
{{- if .Gainer}}
📈 {{.Gainer}}{{end}}
{{- if .Slider}}
📉 {{.Slider}}{{end}}

{{range .All}}{{.Place}}. {{.Mention}}: {{.Total}}
{{end -}}
{{- if .GameScore}}
{{.GameScore}}{{end}}

Full scores: <{{.Site}}>
{{- if .NextGame}}

{{.NextGame}}{{end}}
