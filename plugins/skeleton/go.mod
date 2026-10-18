module example.com/my-plugin

go 1.24

require github.com/vicktor/sap13ns/pluginsdk v0.0.0

// The SDK is used from a sap13ns checkout. "sap13ns plugin new -sdk DIR"
// points this line at another copy.
replace github.com/vicktor/sap13ns/pluginsdk => ../../pluginsdk
