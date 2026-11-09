package jail

// SystemdUnit is the template unit; the instance is the escaped repository path
// (systemd-escape --path /path/to/repo).
func SystemdUnit() string { return unit }

const unit = `[Unit]
Description=p0ssess OpenCode jail for %f
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%h/.config/p0ssess/bin/p0ssess jail up --repo %f
ExecStop=%h/.config/p0ssess/bin/p0ssess jail down --repo %f
TimeoutStartSec=180

[Install]
WantedBy=default.target
`
