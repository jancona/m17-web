package activity

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// SaveInterval is how often a changed log is written to the SD card.
const SaveInterval = 5 * time.Minute

// Subscribe listens to <name>/json on the broker at addr (host:port) and
// feeds the log until ctx is done, saving it every SaveInterval and at the
// end. It reconnects by itself, so a broker that's down or restarting is
// fine.
func (l *Log) Subscribe(ctx context.Context, addr, name string, log *slog.Logger) {
	topic := name + "/json"
	host, _ := os.Hostname()
	id := "m17-admin-" + host
	if len(id) > 23 {
		id = id[:23]
	}
	opts := mqtt.NewClientOptions().
		AddBroker("tcp://" + addr).
		SetClientID(id).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10 * time.Second).
		SetMaxReconnectInterval(time.Minute).
		SetOnConnectHandler(func(c mqtt.Client) {
			// A clean session forgets subscriptions, so subscribe on every
			// connection. The broker sends the retained link message.
			t := c.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) {
				if err := l.Handle(m.Payload()); err != nil {
					log.Warn("activity: bad message", "topic", m.Topic(), "err", err)
				}
			})
			if t.WaitTimeout(10*time.Second) && t.Error() == nil {
				l.SetBroker(true)
				log.Info("activity: subscribed", "broker", addr, "topic", topic)
			} else {
				log.Warn("activity: subscribe failed", "topic", topic, "err", t.Error())
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			l.SetBroker(false)
			log.Info("activity: broker connection lost", "err", err)
		})
	c := mqtt.NewClient(opts)
	c.Connect() // retries in the background

	tick := time.NewTicker(SaveInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			c.Disconnect(250)
			l.SetBroker(false)
			if err := l.Save(); err != nil {
				log.Warn("activity: saving", "err", err)
			}
			return
		case <-tick.C:
			if err := l.Save(); err != nil {
				log.Warn("activity: saving", "err", err)
			}
		}
	}
}

// Describe is a one-line description of what the link status means, for
// the activity page.
func (l *Link) Describe() string {
	if l == nil {
		return "unknown"
	}
	where := l.Reflector
	if l.Module != "" {
		where += " " + l.Module
	}
	switch l.Action {
	case "linking":
		if l.Reason == "established" {
			return "linked to " + where
		}
		return "linking to " + where
	case "relinking":
		return "lost " + where + ", relinking"
	case "failed":
		return fmt.Sprintf("can't link to %s (%s)", where, l.Reason)
	case "unlinked":
		if l.Reflector == "" {
			return "not linked"
		}
		return "unlinked from " + where
	}
	return l.Action
}

// ModemState describes the modem for the activity page: its description
// when open, "closed", or "unknown" when the gateway hasn't said since we
// subscribed (it doesn't retain its Modem message).
func (s Status) ModemState() string {
	switch {
	case s.ModemOpen == nil:
		return "unknown"
	case !*s.ModemOpen:
		return "closed"
	case s.Modem != "":
		return s.Modem
	}
	return "open"
}
