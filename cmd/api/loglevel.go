package main

// levelForEnvironment picks the log level from app.environment, so no new
// (mandatory) config key is needed: development is chatty, everything else
// logs info and above.
func levelForEnvironment(env string) string {
	if env == "development" {
		return "debug"
	}
	return "info"
}
