import unittest

from domain import TaskOwnership
from fixtures import unclaimed_task


class TaskOwnershipTest(unittest.TestCase):
    def setUp(self):
        self.task: TaskOwnership
        self.task, self.squad_a, self.squad_b = unclaimed_task()

    def test_ac_1_first_squad_claims_task(self):
        self.assertTrue(self.task.claim(self.squad_a))
        self.assertEqual(self.task.assigned_squad, self.squad_a)

    def test_ac_2_second_squad_cannot_replace_first(self):
        self.task.claim(self.squad_a)
        self.assertFalse(self.task.claim(self.squad_b))
        self.assertEqual(self.task.assigned_squad, self.squad_a)
