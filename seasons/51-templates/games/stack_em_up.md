🪵 **Stack 'Em Up**: +1 for finishing · fastest +3, second +2 · +2 for a clean run (no drops)
{{range .Players}}{{if .Points}}{{with .TribeEmoji}}{{.}} {{end}}{{.Name}} (+{{.Points}}) · {{if .Finished}}{{.Time}}{{else}}{{.Height}}/8 stacked{{end}}{{if eq .PlacePoints 3}} 🥇 +3{{else if eq .PlacePoints 2}} 🥈 +2{{end}}{{if .CleanPoints}} 🔥 clean +{{.CleanPoints}}{{end}}
{{end}}{{end -}}
