"""Native authoring API checks; run with Python and Pydantic installed."""
import unittest
from harness import Agent, Workflow


class SystemPromptAppendTests(unittest.TestCase):
    def test_agent_bind_and_repair_preserve_additions(self):
        flow = Workflow("prompts")
        workspace = flow.worktree("workspace")
        Agent("Review", None, system_prompt_append="Reusable guidance").bind(
            flow, "review", workspace=workspace)
        flow.agent("implement", workspace=workspace, prompt="Implement",
                   system_prompt_append="Implementation guidance")
        flow.repeat_check("check", ["true"], workspace=workspace,
                          repair_prompt="Fix failures", after="implement",
                          repair_system_prompt_append="Repair guidance")
        self.assertEqual(flow.steps[1]["spec"]["system_prompt_append"], "Reusable guidance")
        self.assertEqual(flow.steps[2]["spec"]["system_prompt_append"], "Implementation guidance")
        self.assertEqual(flow.steps[3]["spec"]["repair_system_prompt_append"], "Repair guidance")

    def test_non_strings_rejected_before_export(self):
        flow = Workflow("bad")
        for invalid in (None, False, 123, ["text"]):
            with self.subTest(invalid=invalid):
                with self.assertRaises(TypeError):
                    Agent("Review", None, system_prompt_append=invalid)
                with self.assertRaises(TypeError):
                    flow.agent("bad", workspace="w", prompt="p", system_prompt_append=invalid)
                with self.assertRaises(TypeError):
                    flow.repeat_check("bad", ["true"], workspace="w", repair_prompt="p",
                                      after="w", repair_system_prompt_append=invalid)
        self.assertEqual(flow.steps, [])


if __name__ == "__main__":
    unittest.main()
