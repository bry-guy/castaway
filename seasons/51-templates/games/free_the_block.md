🧱 **Free the Block**: +1 for solving · fewest moves +3, second +2 · +5 for the 25-move optimum
{{range .Players}}{{if .Solved}}{{with .TribeEmoji}}{{.}} {{end}}{{.Name}} (+{{.Points}}) · {{.Moves}} {{if eq .Moves 1}}move{{else}}moves{{end}}{{if .PlacePoints}} 🥇 +{{.PlacePoints}}{{end}}{{if .Perfect}} 🎯 optimum +5{{end}}
{{end}}{{end -}}
