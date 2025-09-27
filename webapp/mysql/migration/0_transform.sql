-- delivery plan を作るときの orders の取得用
ALTER TABLE orders
    LOCK = SHARED,
    ADD COLUMN shipped_status_code TINYINT
        AS (CASE shipped_status
            -- completed < delivering < shipping の順番
                WHEN 'completed' THEN 0
                WHEN 'delivering' THEN 1
                WHEN 'shipping' THEN 2
            END
            ) STORED;

CREATE TABLE IF NOT EXISTS shipping_orders (
    order_id INT UNSIGNED NOT NULL,
    product_id INT UNSIGNED NOT NULL,
    PRIMARY KEY (order_id),
    KEY idx_shipping_orders_product_id_order_id (product_id, order_id),
    CONSTRAINT fk_shipping_orders_order FOREIGN KEY (order_id) REFERENCES orders(order_id) ON DELETE CASCADE,
    CONSTRAINT fk_shipping_orders_product FOREIGN KEY (product_id) REFERENCES products(product_id) ON DELETE CASCADE
) ENGINE=InnoDB
DEFAULT CHARSET=utf8mb4
COLLATE=utf8mb4_0900_ai_ci;

TRUNCATE TABLE shipping_orders;

INSERT INTO shipping_orders (order_id, product_id)
SELECT o.order_id, o.product_id
FROM orders o
WHERE o.shipped_status = 'shipping'
ON DUPLICATE KEY UPDATE product_id = VALUES(product_id);
