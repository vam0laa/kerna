package main

import (
	"fmt"
	"strings"
)

var subcommands = []struct{ name, en, tr string }{
	{"watch", "live view", "canlı görünüm"},
	{"history", "problems over time, --today, --at", "zaman içinde sorunlar, --today, --at"},
	{"sleep", "suspend problems and drain while asleep", "uyku sorunları ve uykuda tüketim"},
	{"why", "look closely at one program", "tek bir programa yakından bak"},
	{"why-froze", "why did the last session end badly", "son oturum neden kötü bitti"},
	{"snapshot", "save a measurement", "ölçüm kaydet"},
	{"compare", "compare with a saved measurement", "kayıtlı ölçümle karşılaştır"},
	{"waybar", "one JSON line for a Waybar module", "Waybar modülü için tek satır JSON"},
	{"ignore", "mute a finding", "bir bulguyu sustur"},
	{"unignore", "bring a muted finding back", "susturulan bulguyu geri getir"},
	{"notify", "desktop notifications on or off", "masaüstü bildirimleri aç/kapat"},
	{"service", "install or remove the history recorder", "geçmiş kaydını kur ya da kaldır"},
	{"check", "what kerna can see on this machine", "kerna bu makinede neyi görebiliyor"},
	{"completion", "print a shell completion script", "kabuk tamamlama betiği yazdır"},
	{"version", "print the version", "sürümü yazdır"},
}

// Completion returns the completion script for fish, bash or zsh.
func Completion(shell string) (string, error) {
	ids := strings.Join(knownIDs(), " ")
	var names []string
	for _, c := range subcommands {
		names = append(names, c.name)
	}
	all := strings.Join(names, " ")
	switch shell {
	case "fish":
		var b strings.Builder
		b.WriteString("# kerna completion for fish: kerna completion fish | source\ncomplete -c kerna -f\n")
		for _, c := range subcommands {
			fmt.Fprintf(&b, "complete -c kerna -n '__fish_use_subcommand' -a %s -d '%s'\n", c.name, c.en)
		}
		b.WriteString("complete -c kerna -l json -d 'print JSON'\ncomplete -c kerna -s n -d 'sampling window in seconds' -r\n")
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from history' -l today -d 'a summary of today'\n")
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from history' -l since -d 'how far back, e.g. 3d' -r\n")
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from history' -l at -d 'one moment, e.g. 03:12' -r\n")
		fmt.Fprintf(&b, "complete -c kerna -n '__fish_seen_subcommand_from ignore unignore' -a '%s'\n", ids)
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from service' -a 'install uninstall'\n")
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from notify' -a 'on off'\n")
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from completion' -a 'fish bash zsh'\n")
		b.WriteString("complete -c kerna -n '__fish_seen_subcommand_from why' -a '(ps -eo comm= | sort -u)'\n")
		return b.String(), nil
	case "bash":
		return fmt.Sprintf(`# kerna completion for bash: source <(kerna completion bash)
_kerna() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"; prev="${COMP_WORDS[COMP_CWORD-1]}"
  case "$prev" in
    ignore|unignore) COMPREPLY=( $(compgen -W "%s" -- "$cur") ); return ;;
    service) COMPREPLY=( $(compgen -W "install uninstall" -- "$cur") ); return ;;
    notify) COMPREPLY=( $(compgen -W "on off" -- "$cur") ); return ;;
    completion) COMPREPLY=( $(compgen -W "fish bash zsh" -- "$cur") ); return ;;
    why) COMPREPLY=( $(compgen -W "$(ps -eo comm= | sort -u | tr ' ' '_')" -- "$cur") ); return ;;
  esac
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "%s --json -n" -- "$cur") )
  else
    COMPREPLY=( $(compgen -W "--json -n --today --since --at" -- "$cur") )
  fi
}
complete -F _kerna kerna
`, ids, all), nil
	case "zsh":
		var b strings.Builder
		b.WriteString("#compdef kerna\n# kerna completion for zsh: kerna completion zsh > ~/.zfunc/_kerna\n_kerna() {\n  local -a cmds\n  cmds=(\n")
		for _, c := range subcommands {
			fmt.Fprintf(&b, "    '%s:%s'\n", c.name, c.en)
		}
		b.WriteString("  )\n  case $words[2] in\n")
		fmt.Fprintf(&b, "    ignore|unignore) compadd %s ;;\n", ids)
		b.WriteString("    service) compadd install uninstall ;;\n    notify) compadd on off ;;\n    completion) compadd fish bash zsh ;;\n    *) _describe 'command' cmds ;;\n  esac\n}\n_kerna \"$@\"\n")
		return b.String(), nil
	}
	return "", fmt.Errorf("%s", L("shell must be fish, bash or zsh", "kabuk fish, bash ya da zsh olmalı"))
}
