package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/kubeshark/demo-apps/gen/inventory/v1"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

const restockBelow = 10

type Service struct {
	inventoryv1.UnimplementedInventoryServiceServer
	stock        *mongo.Collection
	reservations *mongo.Collection
}

func NewService(ctx context.Context, mongoURI string) (*Service, error) {
	// The dissector decodes OP_MSG only, so wire compression stays off: the
	// URI must not carry compressors= and none is set here.
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, err
	}
	if err := httpx.Retry(ctx, "mongodb", 3*time.Second, func() error { return client.Ping(ctx, nil) }); err != nil {
		return nil, err
	}
	db := client.Database("inventory")
	s := &Service{stock: db.Collection("stock"), reservations: db.Collection("reservations")}
	return s, s.seed(ctx)
}

func (s *Service) seed(ctx context.Context) error {
	for _, it := range Items() {
		_, err := s.stock.UpdateOne(ctx,
			bson.M{"_id": it.SKU},
			bson.M{
				"$set":         bson.M{"name": it.Name, "warehouse": it.Warehouse},
				"$setOnInsert": bson.M{"available": it.Available},
			},
			options.UpdateOne().SetUpsert(true))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Register(srv *grpc.Server) {
	inventoryv1.RegisterInventoryServiceServer(srv, s)
}

type stockDoc struct {
	SKU       string `bson:"_id"`
	Name      string `bson:"name"`
	Warehouse string `bson:"warehouse"`
	Available int64  `bson:"available"`
}

func (s *Service) GetStock(ctx context.Context, req *inventoryv1.GetStockRequest) (*inventoryv1.GetStockResponse, error) {
	var doc stockDoc
	err := s.stock.FindOne(ctx, bson.M{"_id": req.GetSku()}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, status.Errorf(codes.NotFound, "no stock item %s", req.GetSku())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "mongodb: %v", err)
	}
	if d := req.GetDelayMs(); d > 0 {
		select {
		case <-time.After(time.Duration(d) * time.Millisecond):
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	return &inventoryv1.GetStockResponse{Sku: doc.SKU, Name: doc.Name, Available: doc.Available, Warehouse: doc.Warehouse}, nil
}

func (s *Service) Reserve(ctx context.Context, req *inventoryv1.ReserveRequest) (*inventoryv1.ReserveResponse, error) {
	if req.GetQuantity() <= 0 || req.GetOrderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "quantity must be positive and order_id set")
	}
	var doc stockDoc
	err := s.stock.FindOneAndUpdate(ctx,
		bson.M{"_id": req.GetSku(), "available": bson.M{"$gte": req.GetQuantity()}},
		bson.M{"$inc": bson.M{"available": -req.GetQuantity()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot reserve %d of %s: insufficient stock", req.GetQuantity(), req.GetSku())
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "mongodb: %v", err)
	}
	res, err := s.reservations.InsertOne(ctx, bson.M{
		"order_id": req.GetOrderId(), "sku": req.GetSku(), "quantity": req.GetQuantity(), "created": time.Now(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "mongodb: %v", err)
	}
	if doc.Available < restockBelow {
		// Keep the demo running: a real warehouse would receive a delivery.
		if _, err := s.stock.UpdateOne(ctx, bson.M{"_id": doc.SKU}, bson.M{"$inc": bson.M{"available": 100}}); err != nil {
			return nil, status.Errorf(codes.Internal, "mongodb: %v", err)
		}
		doc.Available += 100
	}
	return &inventoryv1.ReserveResponse{ReservationId: fmt.Sprint(res.InsertedID), Remaining: doc.Available}, nil
}
