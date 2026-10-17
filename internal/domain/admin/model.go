package admin

import "time"

// DailyVolumePoint is a single time-bucketed aggregate of contribution and
// payout volume for one calendar day.
type DailyVolumePoint struct {
	Date               time.Time `json:"date"`
	ContributionVolume float64   `json:"contributionVolume"`
	PayoutVolume       float64   `json:"payoutVolume"`
}

// Metrics is the platform-wide aggregate snapshot returned by /admin/metrics.
// Volumes are reported in Arc USDC as stored on-chain; totalVolumeUSD equals
// the USDC volume until additional assets are supported.
type Metrics struct {
	TotalUsers         int                `json:"totalUsers"`
	TotalCircles       int                `json:"totalCircles"`
	ActiveCircles      int                `json:"activeCircles"`
	TotalContributions int                `json:"totalContributions"`
	TotalPayouts       int                `json:"totalPayouts"`
	ActiveUsers        int                `json:"activeUsers"`
	NewUsers30d        int                `json:"newUsers30d"`
	ContributionVolume float64            `json:"contributionVolume"`
	PayoutVolume       float64            `json:"payoutVolume"`
	TotalVolumeUSD     float64            `json:"totalVolumeUSD"`
	VolumeUSD30d       float64            `json:"volumeUSD30d"`
	DailyVolume        []DailyVolumePoint `json:"dailyVolume"`
}
