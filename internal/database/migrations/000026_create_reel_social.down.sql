DROP TABLE IF EXISTS social.reel_comments;
DROP TABLE IF EXISTS social.reel_likes;

ALTER TABLE seller.reels
    DROP COLUMN IF EXISTS comment_count,
    DROP COLUMN IF EXISTS like_count;

DROP SCHEMA IF EXISTS social;
