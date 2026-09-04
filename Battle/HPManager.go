package main

// HPUpdate is broadcast when a player's HP changes
type HPUpdate struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	HP     int    `json:"hp"`
}

// DeathMessage is broadcast when a player's HP reaches 0
type DeathMessage struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

const (
	InitialHP = 100
	HitDamage = 20
)

// WelcomeMessage is sent to a single newly-registered client so it can learn its server-side id
type WelcomeMessage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
