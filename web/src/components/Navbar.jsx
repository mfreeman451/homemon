import React from 'react';
import { Link } from 'react-router-dom';
import logo from '../../public/serviceRadar.svg';
// Import the Sun/Moon icons from lucide-react
import { Sun, Moon } from 'lucide-react';

function Navbar({ darkMode, setDarkMode }) {
  const handleToggleDarkMode = () => {
    setDarkMode(!darkMode);
  };

  return (
      <nav className="bg-white dark:bg-gray-800 shadow-lg transition-colors">
        <div className="container mx-auto px-6 py-4">
          <div className="flex items-center justify-between">
            <div className="flex items-center">
              <img src={logo} alt="logo" className="w-10 h-10" />
              <Link
                  to="/"
                  className="text-xl font-bold text-gray-800 dark:text-gray-200 ml-2 transition-colors"
              >
                ServiceRadar
              </Link>
            </div>
            <div className="flex items-center space-x-4">
              <Link
                  to="/"
                  className="text-gray-600 dark:text-gray-300 hover:text-gray-800 dark:hover:text-gray-100 transition-colors"
              >
                Dashboard
              </Link>
              <Link
                  to="/nodes"
                  className="text-gray-600 dark:text-gray-300 hover:text-gray-800 dark:hover:text-gray-100 transition-colors"
              >
                Nodes
              </Link>
              {/* Dark mode toggle icon */}
              <button
                  onClick={handleToggleDarkMode}
                  className="inline-flex items-center justify-center p-2
                         rounded-md transition-colors
                         bg-gray-100 dark:bg-gray-700
                         hover:bg-gray-200 dark:hover:bg-gray-600
                         text-gray-600 dark:text-gray-200
                         border border-gray-300 dark:border-gray-600"
                  aria-label="Toggle Dark Mode"
              >
                {darkMode ? (
                    <Sun className="h-5 w-5" />
                ) : (
                    <Moon className="h-5 w-5" />
                )}
              </button>
            </div>
          </div>
        </div>
      </nav>
  );
}

export default Navbar;
