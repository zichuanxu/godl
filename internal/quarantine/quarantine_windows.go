package quarantine

import "os"

// Mark writes the Zone.Identifier stream for the Internet zone.
func Mark(path, sourceURL string) error {
	body := "[ZoneTransfer]\r\nZoneId=3\r\n"
	if o := origin(sourceURL); o != "" {
		body += "HostUrl=" + o + "\r\n"
	}
	return os.WriteFile(path+":Zone.Identifier", []byte(body), 0o644)
}
