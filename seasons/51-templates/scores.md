## Castaway Season {{.Season}}: Week {{.Week}} Scores

{{if .Boots}}{{.Boots}}

{{end}}👑 {{.Leader}}
{{- if .Gainer}}
📈 {{.Gainer}}{{end}}
{{- if .Slider}}
📉 {{.Slider}}{{end}}

{{range .All}}{{.Place}}. {{.Mention}}: {{.Total}}
{{end -}}
{{- if .Challenges}}
🏝️ **Challenges**: {{range $i, $c := .Challenges}}{{if $i}} · {{end}}{{$c}}{{end}}
{{end}}
{{- if .GameScore}}
{{.GameScore}}{{end}}

Full scores: <{{.Site}}>
{{- if .NextGame}}

{{.NextGame}}{{end}}
