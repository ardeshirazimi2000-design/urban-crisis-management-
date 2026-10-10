-- 0007_guidance: approved safety guidance for the citizens' crisis assistant. Every text a citizen can read
-- went through two people: an author writes a version, a different approver publishes it. Published
-- versions are never edited; a correction is a new version. The citizen app matches questions on the
-- device, so questions are never sent to or stored by the server.
CREATE TABLE guidance_cards (
    id         uuid PRIMARY KEY,
    slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9-]{2,60}$'),
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    retired_reason text,
    retired_seq    bigint                       -- knowledge-base version at which the card was withdrawn
);

-- One counter for every change to what citizens see (publish or withdraw): the knowledge-base version.
CREATE SEQUENCE guidance_publish_seq;

CREATE TABLE guidance_versions (
    id            uuid PRIMARY KEY,
    card_id       uuid NOT NULL REFERENCES guidance_cards(id),
    version       integer NOT NULL,
    title         text NOT NULL,
    category      text NOT NULL,
    keywords      text[] NOT NULL,
    body          text NOT NULL,
    actions       text[] NOT NULL DEFAULT '{}',
    emergency     boolean NOT NULL DEFAULT false,
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','superseded')),
    published_seq bigint,                       -- knowledge-base version at which this text went live
    author_id     uuid REFERENCES users(id),
    reviewer_id   uuid REFERENCES users(id),
    review_reason text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    reviewed_at   timestamptz,
    UNIQUE (card_id, version)
);
CREATE UNIQUE INDEX guidance_one_pending ON guidance_versions (card_id) WHERE status = 'pending';
CREATE UNIQUE INDEX guidance_one_published ON guidance_versions (card_id) WHERE status = 'approved';
