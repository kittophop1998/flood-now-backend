package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/importantplace"
	"floodnow-api/internal/ports"
)

type ImportantPlaceRepository struct {
	db *sql.DB
}

func NewImportantPlaceRepository(db *sql.DB) *ImportantPlaceRepository {
	return &ImportantPlaceRepository{db: db}
}

const importantPlaceColumns = `p.id, p.name, p.category, p.latitude, p.longitude, p.address, p.status,
	p.description, p.contact, p.source, p.created_by_device, p.created_at, p.updated_at`

func scanImportantPlace(row rowScanner) (*importantplace.Place, error) {
	var p importantplace.Place
	if err := row.Scan(&p.ID, &p.Name, &p.Category, &p.Latitude, &p.Longitude, &p.Address, &p.Status,
		&p.Description, &p.Contact, &p.Source, &p.CreatedByDevice, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func (repo *ImportantPlaceRepository) List(ctx context.Context, filter ports.ImportantPlaceFilter) ([]importantplace.Place, error) {
	var b queryBuilder
	if filter.BBox != nil {
		b.add(b.bboxSQL("p.latitude", "p.longitude", *filter.BBox))
	}
	cats := make([]string, len(filter.Categories))
	for i, c := range filter.Categories {
		cats[i] = string(c)
	}
	b.inList("p.category", cats)
	statuses := make([]string, len(filter.Statuses))
	for i, s := range filter.Statuses {
		statuses[i] = string(s)
	}
	b.inList("p.status", statuses)

	rows, err := repo.db.QueryContext(ctx, `SELECT `+importantPlaceColumns+` FROM important_places p`+b.whereSQL()+
		` ORDER BY p.updated_at DESC LIMIT `+b.arg(filter.Limit), b.args...)
	if err != nil {
		return nil, fmt.Errorf("list important places: %w", err)
	}
	defer rows.Close()
	out := []importantplace.Place{}
	for rows.Next() {
		p, err := scanImportantPlace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan important place: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (repo *ImportantPlaceRepository) Get(ctx context.Context, id uuid.UUID) (*importantplace.Place, error) {
	p, err := scanImportantPlace(repo.db.QueryRowContext(ctx, `SELECT `+importantPlaceColumns+` FROM important_places p WHERE p.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get important place: %w", err)
	}
	return p, nil
}

func (repo *ImportantPlaceRepository) Create(ctx context.Context, p *importantplace.Place) error {
	_, err := repo.db.ExecContext(ctx, `
		INSERT INTO important_places (id, name, category, latitude, longitude, address, status, description, contact, source,
			created_by_device, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, p.Name, p.Category, p.Latitude, p.Longitude, p.Address, p.Status, p.Description, p.Contact, p.Source,
		p.CreatedByDevice, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert important place: %w", err)
	}
	return nil
}

func (repo *ImportantPlaceRepository) Update(ctx context.Context, p *importantplace.Place) error {
	_, err := repo.db.ExecContext(ctx, `
		UPDATE important_places SET name = $1, category = $2, latitude = $3, longitude = $4, address = $5, status = $6,
			description = $7, contact = $8, source = $9, updated_at = $10
		WHERE id = $11`,
		p.Name, p.Category, p.Latitude, p.Longitude, p.Address, p.Status, p.Description, p.Contact, p.Source, p.UpdatedAt, p.ID)
	if err != nil {
		return fmt.Errorf("update important place: %w", err)
	}
	return nil
}

func (repo *ImportantPlaceRepository) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := repo.db.ExecContext(ctx, `DELETE FROM important_places WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete important place: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (repo *ImportantPlaceRepository) CountByDeviceSince(ctx context.Context, deviceID string, since time.Time) (int, error) {
	var n int
	err := repo.db.QueryRowContext(ctx,
		`SELECT count(*) FROM important_places WHERE created_by_device = $1 AND created_at >= $2`, deviceID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count important places by device: %w", err)
	}
	return n, nil
}
