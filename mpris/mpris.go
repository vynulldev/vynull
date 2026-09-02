// SPDX-License-Identifier: GPL-3.0-or-later

// Package mpris publishes the audible deck's now-playing state as an MPRIS
// player on the D-Bus session bus (org.mpris.MediaPlayer2.vynull), so the
// desktop's media surfaces pick it up with no configuration: GNOME/KDE
// media controls and lock screens, playerctl, waybar/polybar modules, and
// KDE Connect's phone mirroring.
//
// Vynull is not a player — it observes decks — so the published player is
// deliberately read-only: every capability (CanPlay, CanPause, CanControl,
// ...) is false and the control methods are no-ops, which the MPRIS spec
// supports. Clients render the metadata without working transport controls.
package mpris

import (
	"context"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	busName    = "org.mpris.MediaPlayer2.vynull"
	objectPath = "/org/mpris/MediaPlayer2"
	rootIface  = "org.mpris.MediaPlayer2"
	playerFace = "org.mpris.MediaPlayer2.Player"
)

// NowPlaying is the snapshot the publisher renders into MPRIS properties.
type NowPlaying struct {
	Playing      bool
	DeviceNumber uint8
	TrackID      uint32
	Title        string
	Artist       string
	DurationMs   uint32
	PositionMs   uint32 // 0 = unknown
	ArtURL       string // absolute URL, or ""
}

// playerMethods are the org.mpris.MediaPlayer2.Player controls. All are
// advertised as unavailable and do nothing; they exist because the
// interface requires them. Exported via a method table rather than struct
// methods so the MPRIS-mandated names (Seek, ...) don't trip go vet's
// standard-method-signature check.
var playerMethods = map[string]interface{}{
	"Next":        func() *dbus.Error { return nil },
	"Previous":    func() *dbus.Error { return nil },
	"Pause":       func() *dbus.Error { return nil },
	"PlayPause":   func() *dbus.Error { return nil },
	"Stop":        func() *dbus.Error { return nil },
	"Play":        func() *dbus.Error { return nil },
	"Seek":        func(x int64) *dbus.Error { return nil },
	"SetPosition": func(o dbus.ObjectPath, x int64) *dbus.Error { return nil },
	"OpenUri":     func(s string) *dbus.Error { return nil },
}

// rootMethods implement org.mpris.MediaPlayer2. Quit/Raise are advertised
// unavailable and do nothing.
var rootMethods = map[string]interface{}{
	"Raise": func() *dbus.Error { return nil },
	"Quit":  func() *dbus.Error { return nil },
}

// Publisher owns the bus connection and the polling loop.
type Publisher struct {
	conn  *dbus.Conn
	props *prop.Properties
	last  NowPlaying
}

// Start connects to the session bus, claims the MPRIS name, and begins
// polling snapshot at the given interval, updating properties (and emitting
// PropertiesChanged) whenever the audible deck's state changes. Returns an
// error when there is no session bus (headless boxes) — the caller should
// log it at debug level and move on; MPRIS is strictly optional.
func Start(ctx context.Context, snapshot func() NowPlaying, interval time.Duration) (*Publisher, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		if err == nil {
			err = fmt.Errorf("name %s already owned", busName)
		}
		return nil, err
	}

	p := &Publisher{conn: conn}

	propsSpec := map[string]map[string]*prop.Prop{
		rootIface: {
			"CanQuit":             {Value: false, Emit: prop.EmitFalse},
			"CanRaise":            {Value: false, Emit: prop.EmitFalse},
			"HasTrackList":        {Value: false, Emit: prop.EmitFalse},
			"Identity":            {Value: "Vynull", Emit: prop.EmitFalse},
			"SupportedUriSchemes": {Value: []string{}, Emit: prop.EmitFalse},
			"SupportedMimeTypes":  {Value: []string{}, Emit: prop.EmitFalse},
		},
		playerFace: {
			"PlaybackStatus": {Value: "Stopped", Emit: prop.EmitTrue},
			"Rate":           {Value: 1.0, Emit: prop.EmitFalse},
			"Metadata":       {Value: map[string]dbus.Variant{}, Emit: prop.EmitTrue},
			"Volume":         {Value: 1.0, Emit: prop.EmitFalse},
			// Position deliberately EmitFalse: the spec says position moves
			// without change signals; clients poll it.
			"Position":      {Value: int64(0), Emit: prop.EmitFalse},
			"MinimumRate":   {Value: 1.0, Emit: prop.EmitFalse},
			"MaximumRate":   {Value: 1.0, Emit: prop.EmitFalse},
			"CanGoNext":     {Value: false, Emit: prop.EmitFalse},
			"CanGoPrevious": {Value: false, Emit: prop.EmitFalse},
			"CanPlay":       {Value: false, Emit: prop.EmitFalse},
			"CanPause":      {Value: false, Emit: prop.EmitFalse},
			"CanSeek":       {Value: false, Emit: prop.EmitFalse},
			"CanControl":    {Value: false, Emit: prop.EmitFalse},
		},
	}
	props, err := prop.Export(conn, objectPath, propsSpec)
	if err != nil {
		conn.Close()
		return nil, err
	}
	p.props = props

	if err := conn.ExportMethodTable(rootMethods, objectPath, rootIface); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.ExportMethodTable(playerMethods, objectPath, playerFace); err != nil {
		conn.Close()
		return nil, err
	}
	// Introspection keeps busctl/d-feet/playerctl discovery working.
	node := &introspect.Node{
		Name: objectPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{Name: rootIface},
			{Name: playerFace},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(node), objectPath,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		conn.Close()
		return nil, err
	}

	go p.loop(ctx, snapshot, interval)
	return p, nil
}

func (p *Publisher) loop(ctx context.Context, snapshot func() NowPlaying, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	defer p.conn.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.update(snapshot())
		}
	}
}

// update publishes np, emitting change signals only when something the
// clients render actually changed. Position updates silently every tick.
func (p *Publisher) update(np NowPlaying) {
	p.props.SetMust(playerFace, "Position", int64(np.PositionMs)*1000) // µs

	changed := np.Playing != p.last.Playing ||
		np.TrackID != p.last.TrackID ||
		np.DeviceNumber != p.last.DeviceNumber ||
		np.Title != p.last.Title ||
		np.Artist != p.last.Artist
	if !changed {
		return
	}
	p.last = np

	status := "Stopped"
	if np.Playing {
		status = "Playing"
	}
	// Metadata BEFORE PlaybackStatus: clients react to the status change and
	// immediately read metadata, so it must already be consistent.
	p.props.SetMust(playerFace, "Metadata", metadataFrom(np))
	p.props.SetMust(playerFace, "PlaybackStatus", status)
}

// metadataFrom renders the MPRIS metadata dict. The track id object path
// encodes deck + track so consecutive loads of the same track on different
// decks still register as a change.
//
// The dict ALWAYS carries the same key set: godbus's prop package stores
// map properties with dbus.Store into the existing map, which merges keys
// and never deletes — a smaller dict would leave the previous track's
// fields behind (verified against a live bus). Constant keys make every
// update a full overwrite.
func metadataFrom(np NowPlaying) map[string]dbus.Variant {
	trackid := dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack")
	artists := []string{}
	if np.Playing {
		trackid = dbus.ObjectPath(fmt.Sprintf("/dev/vynull/deck%d/track%d", np.DeviceNumber, np.TrackID))
		if np.Artist != "" {
			artists = []string{np.Artist}
		}
	}
	title, artURL, lengthUs := "", "", int64(0)
	if np.Playing {
		title = np.Title
		artURL = np.ArtURL
		lengthUs = int64(np.DurationMs) * 1000
	}
	return map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(trackid),
		"xesam:title":   dbus.MakeVariant(title),
		"xesam:artist":  dbus.MakeVariant(artists),
		"mpris:length":  dbus.MakeVariant(lengthUs),
		"mpris:artUrl":  dbus.MakeVariant(artURL),
	}
}
