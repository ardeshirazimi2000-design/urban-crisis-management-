"""Baseline Persian crisis-report scorer.

IMPORTANT (ADR-004): this is a decision-support signal only. It never accepts, rejects or escalates a
report and never triggers alerts. The baseline is a transparent keyword model so its behaviour is
auditable; a trained model may replace it only after per-class precision/recall on labelled Persian
data is reviewed (decision D-06). `type_confidence` is the share of matched evidence for the predicted
class, NOT the probability that the report is true.
"""
from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass, field

MODEL_VERSION = "fa-keyword-baseline-1.0.0"

# Normalise Arabic code points that Persian keyboards emit inconsistently.
_CHAR_MAP = str.maketrans({"ي": "ی", "ك": "ک", "ة": "ه", "ۀ": "ه", "أ": "ا", "إ": "ا", "‌": " ", "ـ": ""})
_DIGITS = str.maketrans("۰۱۲۳۴۵۶۷۸۹٠١٢٣٤٥٦٧٨٩", "01234567890123456789")

TYPE_KEYWORDS: dict[str, list[str]] = {
    "building_collapse": ["ریزش ساختمان", "فرو ریخت", "فروریخت", "تخریب کامل", "آوار", "ساختمان ریخت"],
    "trapped_people": ["زیر آوار", "گیر افتاده", "محبوس", "صدای کمک", "نمیتوانند خارج", "گیر کرده"],
    "structural_damage": ["ترک", "شکاف", "ریزش دیوار", "ریزش بخشی", "کج شده", "ستون", "سقف"],
    "fire": ["آتش", "حریق", "دود", "شعله", "سوختن"],
    "gas_leak": ["بوی گاز", "نشت گاز", "گاز", "انفجار"],
    "injury": ["زخمی", "مجروح", "خونریزی", "شکستگی", "بیهوش", "مصدوم"],
    "road_blocked": ["مسدود", "بسته شده", "راه بند", "انسداد", "خیابان بسته"],
    "flooding": ["آب گرفتگی", "سیل", "ترکیدگی لوله", "آبگرفتگی"],
    "power_outage": ["قطع برق", "برق رفته", "خاموشی", "کابل برق"],
    "water_outage": ["قطع آب", "آب نداریم"],
    "landslide": ["رانش", "ریزش کوه", "لغزش زمین"],
    "hazmat": ["مواد شیمیایی", "بوی تند", "مواد خطرناک", "نشت مواد"],
}

# Urgency cues: life safety first.
URGENCY_KEYWORDS: dict[str, float] = {
    "زیر آوار": 1.0, "گیر افتاده": 0.9, "محبوس": 0.9, "بیهوش": 0.9, "خونریزی": 0.8, "کودک": 0.5,
    "نفس نمیکشد": 1.0, "انفجار": 0.9, "آتش": 0.6, "بوی گاز": 0.7, "سالمند": 0.4, "فوری": 0.4, "زخمی": 0.6,
}
TYPE_BASE_URGENCY = {"trapped_people": 0.9, "building_collapse": 0.8, "gas_leak": 0.7, "fire": 0.6, "injury": 0.6, "hazmat": 0.7}


def normalize(text: str) -> str:
    text = unicodedata.normalize("NFKC", text or "").translate(_CHAR_MAP).translate(_DIGITS)
    text = re.sub(r"[^\w\s]", " ", text)
    return re.sub(r"\s+", " ", text).strip()


@dataclass
class Score:
    predicted_type: str | None
    type_confidence: float | None
    urgency_signal: float
    signals: dict = field(default_factory=dict)
    model_version: str = MODEL_VERSION

    def as_payload(self) -> dict:
        return {
            "predicted_type": self.predicted_type,
            "type_confidence": self.type_confidence,
            "urgency_signal": self.urgency_signal,
            "signals": self.signals,
            "model_version": self.model_version,
        }


def score(description: str, reported_type: str | None = None, media_count: int = 0, possible_duplicates: int = 0) -> Score:
    text = normalize(description)
    hits: dict[str, list[str]] = {}
    for typ, words in TYPE_KEYWORDS.items():
        found = [w for w in words if normalize(w) in text]
        if found:
            hits[typ] = found
    total = sum(len(v) for v in hits.values())
    predicted, confidence = None, None
    if total:
        # Ties are broken by life-safety priority (order of TYPE_KEYWORDS).
        predicted = max(hits, key=lambda t: (len(hits[t]), -list(TYPE_KEYWORDS).index(t)))
        confidence = round(len(hits[predicted]) / total, 3)

    urgency_cues = [w for w in URGENCY_KEYWORDS if normalize(w) in text]
    urgency = max([URGENCY_KEYWORDS[w] for w in urgency_cues] + [TYPE_BASE_URGENCY.get(reported_type or "", 0.2)])

    signals = {
        "keywords": sorted({w for v in hits.values() for w in v}),
        "urgency_cues": urgency_cues,
        "type_disagreement": bool(predicted and reported_type and predicted != reported_type),
        "text_length": len(text),
        # Media count and duplicate counts are context only; they do NOT raise credibility (doc §12.2).
        "media_count": media_count,
        "possible_duplicates": possible_duplicates,
        "low_information": len(text) < 10,
    }
    return Score(predicted, confidence, round(float(urgency), 3), signals)
