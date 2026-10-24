🧱 **Free the Block**: +1 for solving · fewest moves +2, second +1
{{range .Players}}{{if .Solved}}{{with .TribeEmoji}}{{.}} {{end}}{{.Name}} (+{{.Points}}) · {{.Moves}} {{if eq .Moves 1}}move{{else}}moves{{end}}
{{end}}{{end -}}
