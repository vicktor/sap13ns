module github.com/vicktor/sap13ns/plugins/external-links

go 1.24.0

require (
	github.com/vicktor/sap13ns/pluginsdk v0.0.0
	golang.org/x/net v0.50.0
)

require golang.org/x/text v0.34.0 // indirect

replace github.com/vicktor/sap13ns/pluginsdk => ../../pluginsdk
