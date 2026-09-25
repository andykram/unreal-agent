"""Throwaway graph-authoring API. Operations describe work; they do not execute it."""
import json
import sys
from output_model import OutputModel, output_schema
from dataclasses import dataclass

@dataclass(frozen=True)
class OutputRef:
    step: str
    path: tuple = ()

    def field(self, *path):
        return OutputRef(self.step, self.path + tuple(path))

    def equals(self, value):
        if value is not None and type(value) not in (str, bool, int, float):
            raise TypeError("conditions compare scalar JSON values")
        return dict(step=self.step, path=list(self.path), equals=value)


@dataclass(frozen=True)
class Agent:
    """Reusable agent definition. Each binding creates an independent step."""
    prompt: str
    output: type
    skills: tuple = ()
    system_prompt_append: str = ""

    def __post_init__(self):
        if not isinstance(self.system_prompt_append, str):
            raise TypeError("system_prompt_append must be a string")

    def bind(self, flow, id, *, workspace, inputs=None, after=(), when=None):
        dependencies = [after] if isinstance(after, str) else list(after)
        if workspace not in dependencies:
            dependencies.append(workspace)
        return flow.agent(id, workspace=workspace, prompt=self.prompt,
                          output=self.output, skills=self.skills, inputs=inputs,
                          after=dependencies, when=when, system_prompt_append=self.system_prompt_append)


@dataclass(frozen=True)
class Pipeline:
    steps: tuple

    @property
    def last(self):
        return self.steps[-1]

    @property
    def output(self):
        return OutputRef(self.last)


class Workflow:
    def __init__(self, name):
        self.name, self.steps = name, []

    def step(self, id, kind, *, after=(), when=None, **spec):
        if isinstance(after, str):
            after = [after]
        step = dict(id=id, kind=kind, needs=list(after), spec=spec)
        def encode(value):
            if isinstance(value, OutputRef):
                if value.step not in step["needs"]:
                    step["needs"].append(value.step)
                return {"$output": dict(step=value.step, path=list(value.path))}
            if isinstance(value, dict):
                if "$output" in value:
                    raise ValueError("$output is reserved; use flow.ref()")
                return {key: encode(item) for key, item in value.items()}
            if isinstance(value, (list, tuple)):
                return [encode(item) for item in value]
            return value
        if "inputs" in spec:
            step["spec"]["inputs"] = encode(spec["inputs"])
        if when is not None:
            step["when"] = dict(when)
            if when["step"] not in step["needs"]:
                step["needs"].append(when["step"])
        self.steps.append(step)
        return id

    def worktree(self, id, base="current"):
        return self.step(id, "worktree", base=base, backend="worktrunk")

    def agent(self, id, *, workspace, prompt, after=(), when=None, skills=(), output=None, inputs=None, system_prompt_append=""):
        if not isinstance(system_prompt_append, str):
            raise TypeError("system_prompt_append must be a string")
        spec = dict(workspace=workspace, prompt=prompt, skills=self._skill_names(skills),
                    system_prompt_append=system_prompt_append)
        if output is not None:
            spec["output_schema"] = output_schema(output)
        if inputs is not None:
            if not isinstance(inputs, dict):
                raise TypeError("inputs must be a mapping")
            spec["inputs"] = inputs
        return self.step(id, "agent", after=after or [workspace], when=when, **spec)

    def command(self, id, argv, *, workspace, after, when=None):
        return self.step(id, "command", after=after, when=when, workspace=workspace, argv=argv)

    def repeat_check(self, id, argv, *, workspace, repair_prompt,
                     max_repairs=2, after, when=None, repair_skills=(), repair_system_prompt_append=""):
        if not isinstance(repair_system_prompt_append, str):
            raise TypeError("repair_system_prompt_append must be a string")
        if type(max_repairs) is not int or not 0 <= max_repairs <= 10:
            raise ValueError("max_repairs must be an integer from 0 to 10")
        return self.step(id, "repeat_check", after=after, when=when, workspace=workspace,
                         argv=argv, repair_prompt=repair_prompt,
                         max_repairs=max_repairs, repair_skills=self._skill_names(repair_skills),
                         repair_system_prompt_append=repair_system_prompt_append)

    def approval(self, id, *, after, when=None):
        return self.step(id, "approval", after=after, when=when)

    def join(self, id, *, after):
        return self.step(id, "join", after=after)

    def ref(self, step, *path):
        if any(type(part) not in (str, int) or (type(part) is int and part < 0) for part in path):
            raise ValueError("reference paths use field names or nonnegative indexes")
        return OutputRef(step, tuple(path))

    def chain(self, prefix, stages, *, workspace, inputs=None, context=None, after=()):
        steps = []
        values = inputs
        dependencies = after
        for name, agent in stages:
            stage_inputs = dict(values or {})
            if context is not None:
                if "context" in stage_inputs:
                    raise ValueError("chain reserves the context input")
                stage_inputs["context"] = context
            step = agent.bind(self, prefix + "-" + name, workspace=workspace,
                              inputs=stage_inputs, after=dependencies)
            steps.append(step)
            values = {"previous": self.ref(step)}
            dependencies = [step]
        if not steps:
            raise ValueError("chain requires at least one stage")
        return Pipeline(tuple(steps))

    def map_agents(self, prefix, items, agent, *, workspaces, after=()):
        items, workspaces = list(items), list(workspaces)
        if not items or len(items) != len(workspaces):
            raise ValueError("map requires one workspace per item and at least one item")
        return [agent.bind(self, f"{prefix}-{index}", workspace=workspace,
                           inputs={"item": item}, after=after)
                for index, (item, workspace) in enumerate(zip(items, workspaces))]

    def reduce_agent(self, id, steps, agent, *, workspace, inputs=None, after=()):
        steps = list(steps)
        if not steps:
            raise ValueError("reduce requires at least one source")
        values = dict(inputs or {})
        if "results" in values:
            raise ValueError("reduce reserves the results input")
        values["results"] = [self.ref(step) for step in steps]
        return agent.bind(self, id, workspace=workspace, inputs=values, after=after)

    @staticmethod
    def _skill_names(names):
        if isinstance(names, str):
            raise ValueError("skills must be a sequence of names, not a single string")
        names = list(names)
        normalized = []
        for skill in names:
            if isinstance(skill, str) and skill.strip():
                item = skill
            elif (isinstance(skill, dict) and set(skill) == {"name", "arguments"}
                  and isinstance(skill["name"], str) and skill["name"].strip()
                  and isinstance(skill["arguments"], str)):
                item = dict(skill)
            else:
                raise ValueError("skills require names or {name, arguments} descriptors")
            if item not in normalized:
                normalized.append(item)
        return normalized

    def export(self):
        print(json.dumps(dict(version=1, name=self.name, python=sys.version,
                              platform=sys.platform, steps=self.steps), allow_nan=False))
