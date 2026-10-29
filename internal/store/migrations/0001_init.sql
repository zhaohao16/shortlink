CREATE TABLE IF NOT EXISTS t_link (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    seq        BIGINT UNSIGNED NOT NULL COMMENT '发号器序列',
    code       VARCHAR(16)     NOT NULL COMMENT '6 位 base62 短码',
    url        VARCHAR(2048)   NOT NULL COMMENT '原始长链接',
    created_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_code (code),
    UNIQUE KEY uk_seq (seq)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;
