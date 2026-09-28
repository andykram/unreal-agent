"""Real Pydantic models, with compatibility names for the prototype API.

The WASI runtime bundles Pydantic 1.10.26's pure-Python distribution. V2 requires
pydantic-core, which has no published WASI wheel for this CPython runtime.
"""
from copy import deepcopy
from pydantic import BaseModel


if hasattr(BaseModel, "model_json_schema"):
    class OutputModel(BaseModel):
        model_config = {"extra": "forbid"}
else:
    class OutputModel(BaseModel):
        class Config:
            extra = "forbid"

        @classmethod
        def model_json_schema(cls, **kwargs):
            return _v1_schema(cls, **kwargs)

        @classmethod
        def model_validate_json(cls, text, **kwargs):
            return cls.parse_raw(text, **kwargs)

        def model_dump(self, **kwargs):
            return self.dict(**kwargs)


def output_schema(model):
    """Export real Pydantic JSON Schema; reject arbitrary lookalike classes."""
    if not isinstance(model, type) or not issubclass(model, BaseModel):
        raise TypeError("output must be a Pydantic BaseModel class")
    if hasattr(model, "model_json_schema"):
        schema = model.model_json_schema()
        schema.setdefault("$schema", "https://json-schema.org/draft/2020-12/schema")
        return schema
    return _v1_schema(model)


def _v1_schema(model, **kwargs):
    # V1 omits null from Optional schemas. Preserve its generated constraints
    # and definitions, adding null from Pydantic's own resolved ModelField data.
    from pydantic.schema import get_flat_models_from_model, get_model_name_map
    schema = deepcopy(model.schema(**kwargs))
    schema.setdefault("$schema", "http://json-schema.org/draft-07/schema#")
    models = get_flat_models_from_model(model)
    names = get_model_name_map(models)
    definitions = schema.get("definitions", {})
    for nested in models:
        if not issubclass(nested, BaseModel):
            continue
        target = schema if nested is model and "properties" in schema else definitions.get(names[nested], {})
        for name, field in nested.__fields__.items():
            key = field.alias if kwargs.get("by_alias", True) else name
            field_schema = target.get("properties", {}).get(key)
            if field_schema is not None:
                _nullable_field(field, field_schema)
    return schema


def _nullable_field(field, schema):
    children = field.sub_fields or []
    if "items" in schema and children:
        items = schema["items"]
        if isinstance(items, list):
            for child, item in zip(children, items):
                _nullable_field(child, item)
        else:
            _nullable_field(children[0], items)
    elif "anyOf" in schema:
        for child, variant in zip(children, schema["anyOf"]):
            _nullable_field(child, variant)
    elif isinstance(schema.get("additionalProperties"), dict) and children:
        _nullable_field(children[-1], schema["additionalProperties"])
    if field.allow_none:
        original = dict(schema)
        schema.clear()
        for key in ("title", "description", "default"):
            if key in original:
                schema[key] = original.pop(key)
        schema["anyOf"] = [original, {"type": "null"}]
