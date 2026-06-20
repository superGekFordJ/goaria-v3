package utils

import "log"

var SuppressNotifications bool

func Notify(title, message string) {
	if SuppressNotifications {
		return
	}
	log.Printf("[Surge Notification] Title: %s, Message: %s", title, message)
}
