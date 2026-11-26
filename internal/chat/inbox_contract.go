package chat

import "strings"

const (
	// ProducerUser identifies direct human messages from Web or a paired channel.
	ProducerUser = "user"
	// ProducerSubSession identifies child delegations and child-to-parent results/reports.
	ProducerSubSession = "sub_session"
	// ProducerScheduler identifies Automations, dreams, Monitor wake-ups, and Process wake-ups.
	ProducerScheduler = "scheduler"
)

// inboxIdentity splits a submission origin into its producer identity and
// optional direct-channel reply address. Non-user inputs carry no direct
// channel reply address.
func inboxIdentity(origin string) (producer, replyChannel, replyPeer string) {
	switch origin {
	case "task":
		return ProducerSubSession, "", ""
	case "automation", "dream":
		return ProducerScheduler, "", ""
	case "", "web":
		return ProducerUser, "", ""
	default:
		if ch, peer, ok := strings.Cut(origin, ":"); ok && ch != "" && peer != "" {
			return ProducerUser, ch, peer
		}
		return ProducerUser, "", ""
	}
}
