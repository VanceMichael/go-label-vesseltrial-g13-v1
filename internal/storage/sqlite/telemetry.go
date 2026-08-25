package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/fault"
	"github.com/VanceMichael/go-label-vesseltrial-g13-v1/internal/model"
)

func (s *Store) CreateStation(ctx context.Context, code, name string) (model.ShoreStation, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO shore_stations(code,name,status) VALUES(?,?,'offline')`, code, name)
	if err != nil {
		return model.ShoreStation{}, fault.Wrap(fault.Conflict, "station_exists", "shore station already exists", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.ShoreStation{}, fmt.Errorf("read station id: %w", err)
	}
	return model.ShoreStation{ID: id, Code: code, Name: name, Status: "offline"}, nil
}

func (s *Store) IngestTelemetry(ctx context.Context, sample model.TelemetrySample) (model.TelemetrySample, error) {
	now := s.now()
	var saved model.TelemetrySample
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := requireVessel(ctx, tx, sample.VesselID); err != nil {
			return err
		}
		var stationStatus string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM shore_stations WHERE id=?", sample.StationID).Scan(&stationStatus); errors.Is(err, sql.ErrNoRows) {
			return fault.New(fault.NotFound, "station_not_found", "shore station not found")
		} else if err != nil {
			return fmt.Errorf("load station: %w", err)
		}
		if sample.LegID != nil {
			leg, err := loadLeg(ctx, tx, *sample.LegID)
			if err != nil {
				return err
			}
			if leg.VesselID != sample.VesselID || (leg.Status != "released" && leg.Status != "underway") {
				return fault.New(fault.Conflict, "leg_not_active", "telemetry leg is not active for this vessel")
			}
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("telemetry request canceled before persistence: %w", err)
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO telemetry_samples(vessel_id,leg_id,station_id,sequence,battery_pct,speed_knots,latitude,longitude,observed_at,received_at)
VALUES(?,?,?,?,?,?,?,?,?,?)`, sample.VesselID, sample.LegID, sample.StationID, sample.Sequence, sample.BatteryPct, sample.SpeedKnots, sample.Latitude, sample.Longitude, formatTime(sample.ObservedAt), formatTime(now))
		if err != nil {
			return fault.Wrap(fault.Conflict, "telemetry_duplicate", "telemetry sequence was already received", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read telemetry id: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shore_stations SET last_seen_at=?,status='online' WHERE id=?`, formatTime(now), sample.StationID); err != nil {
			return fmt.Errorf("update station freshness: %w", err)
		}
		sample.ID, sample.ReceivedAt = id, now
		saved = sample
		return nil
	})
	return saved, err
}

func (s *Store) ListTelemetry(ctx context.Context, vesselID int64, since time.Time, limit, offset int) ([]model.TelemetrySample, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,vessel_id,leg_id,station_id,sequence,battery_pct,speed_knots,latitude,longitude,observed_at,received_at
FROM telemetry_samples WHERE vessel_id=? AND observed_at>=? ORDER BY observed_at DESC,id DESC LIMIT ? OFFSET ?`, vesselID, formatTime(since), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query telemetry: %w", err)
	}
	defer rows.Close()
	result := make([]model.TelemetrySample, 0, limit)
	for rows.Next() {
		var item model.TelemetrySample
		var leg sql.NullInt64
		var observed, received string
		if err := rows.Scan(&item.ID, &item.VesselID, &leg, &item.StationID, &item.Sequence, &item.BatteryPct, &item.SpeedKnots, &item.Latitude, &item.Longitude, &observed, &received); err != nil {
			return nil, fmt.Errorf("scan telemetry: %w", err)
		}
		item.LegID = nullableInt(leg)
		if item.ObservedAt, err = parseTime(observed); err != nil {
			return nil, err
		}
		if item.ReceivedAt, err = parseTime(received); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) MarkStaleStations(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE shore_stations SET status='stale' WHERE status='online' AND (last_seen_at IS NULL OR last_seen_at<?)`, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("mark stale stations: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) Station(ctx context.Context, id int64) (model.ShoreStation, error) {
	var station model.ShoreStation
	var seen sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,code,name,last_seen_at,status FROM shore_stations WHERE id=?`, id).Scan(&station.ID, &station.Code, &station.Name, &seen, &station.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ShoreStation{}, fault.New(fault.NotFound, "station_not_found", "shore station not found")
	}
	if err != nil {
		return model.ShoreStation{}, fmt.Errorf("load station: %w", err)
	}
	station.LastSeenAt, err = nullableTime(seen)
	return station, err
}
