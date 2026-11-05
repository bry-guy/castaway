🌴 **Tilt Table**: +1 for finishing · fastest +3, second +2 · +1 per ace (a ball in with no rollback)
{{range .Players}}{{if .Points}}{{with .TribeEmoji}}{{.}} {{end}}{{.Name}} (+{{.Points}}) · {{if .Finished}}{{.Time}}{{else}}{{.Holes}}/3 in{{end}}{{if eq .PlacePoints 3}} 🥇 +3{{else if eq .PlacePoints 2}} 🥈 +2{{end}}{{if .Aces}} 🎯 {{.Aces}} {{if eq .Aces 1}}ace{{else}}aces{{end}}{{end}}
{{end}}{{end -}}
