module github.com/tonyrosario/setpoint/cli

go 1.26.4

replace github.com/tonyrosario/setpoint/core => ../core

require (
	github.com/tonyrosario/setpoint/core v0.0.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
)
