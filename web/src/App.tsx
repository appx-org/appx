import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Settings from './pages/Settings';
import Project from './pages/Project';
import Egress from './pages/Egress';
import Shell from './pages/Shell';

/** App is the root component that sets up client-side routing. Unauthenticated
 *  users are directed to /login; authenticated users see the Dashboard at /. */
export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<Dashboard />} />
        <Route path="/projects/:id" element={<Project />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/egress" element={<Egress />} />
        <Route path="/shell" element={<Shell />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
